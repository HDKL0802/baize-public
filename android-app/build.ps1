# Build the Todo app APK without Gradle.
# NOTE 1: aapt2 cannot handle non-ASCII paths, so all tool work happens in an ASCII staging dir.
# NOTE 2: keep this file ASCII-only. PowerShell 5.1 reads .ps1 as ANSI, and non-ASCII bytes
#         inside comments can be mis-parsed (e.g. swallow the following line).
# Usage: powershell -ExecutionPolicy Bypass -File build.ps1
$ErrorActionPreference = "Stop"

# ---------- toolchain ----------
# Prefer the user's own JDK (D:\mc\Java), fall back to a locally installed copy,
# then to the JDK bundled with HBuilderX (amazon-corretto) -- this box has the latter.
$JAVA_HOME = "D:\mc\Java\17-lts"
if (-not (Test-Path "$JAVA_HOME\bin\javac.exe")) {
    $JAVA_HOME = "$env:LOCALAPPDATA\Programs\jdk-17\jdk-17.0.13+11"
}
if (-not (Test-Path "$JAVA_HOME\bin\javac.exe")) {
    $JAVA_HOME = "D:\HBuilderX\plugins\amazon-corretto"
}
if (-not (Test-Path "$JAVA_HOME\bin\javac.exe")) {
    $JAVA_HOME = "$env:BZ_JDK_HOME"
}
# Android SDK: BZ_ANDROID_SDK wins, then the standard machine-wide location,
# then D:\android-sdk (this box), then the copy under the user profile.
$SDK = $env:BZ_ANDROID_SDK
if (-not $SDK) {
    foreach ($cand in @("$env:LOCALAPPDATA\Android\Sdk", "D:\android-sdk", "$env:USERPROFILE\.local\share\android-sdk")) {
        if (Test-Path "$cand\build-tools") { $SDK = $cand; break }
    }
    if (-not $SDK) { $SDK = "$env:USERPROFILE\.local\share\android-sdk" }
}
$BT        = "$SDK\build-tools\34.0.0"
$PLATFORM  = "$SDK\platforms\android-34\android.jar"

foreach ($p in @("$JAVA_HOME\bin\javac.exe", "$JAVA_HOME\bin\jar.exe", "$JAVA_HOME\bin\keytool.exe",
                 "$BT\aapt2.exe", "$BT\d8.bat", "$BT\zipalign.exe", "$BT\apksigner.bat", $PLATFORM)) {
    if (-not (Test-Path $p)) { throw "missing tool: $p" }
}

# d8.bat / apksigner.bat read %JAVA_HOME%; the machine has a stale one (D:\pcl\Java\8-lts\), so override it.
$env:JAVA_HOME = $JAVA_HOME
$env:Path = "$JAVA_HOME\bin;$BT;$env:Path"

# ---------- paths ----------
$SRC     = $PSScriptRoot                       # source project (path may contain non-ASCII chars)
$WEBAPP  = Join-Path $SRC "..\todo-app"        # web frontend
$KERNEL  = Join-Path $SRC "..\core\bin\bzcore-android-arm64"   # Go kernel (linux/arm64)
$STAGE   = "C:\bz-apk-build"                   # ASCII-only staging dir for the toolchain
$OUTNAME = "bz-todo-v0.10.11.apk"

$PKG      = "com.baize.todo"
$MIN_SDK  = 24
$TARGET   = 34
$VER_CODE = 1011
$VER_NAME = "0.10.11"

Write-Host "=== 0. stage clean ===" -ForegroundColor Cyan
if (Test-Path $STAGE) { Remove-Item $STAGE -Recurse -Force }
New-Item -ItemType Directory -Force -Path "$STAGE\classes", "$STAGE\dex", "$STAGE\gen", "$STAGE\assets", "$STAGE\res" | Out-Null

# ---------- copy sources into the staging dir ----------
Copy-Item (Join-Path $SRC "AndroidManifest.xml") $STAGE
# whole res tree (drawable + values + xml/shortcuts.xml)
Get-ChildItem (Join-Path $SRC "res") | Copy-Item -Destination "$STAGE\res" -Recurse -Force
Copy-Item (Join-Path $SRC "java") "$STAGE\java" -Recurse

Write-Host "=== 1. copy web assets ===" -ForegroundColor Cyan
Copy-Item (Join-Path $WEBAPP "index.html") "$STAGE\assets"
Copy-Item (Join-Path $WEBAPP "icon.svg") "$STAGE\assets"
Copy-Item (Join-Path $WEBAPP "manifest.webmanifest") "$STAGE\assets"
foreach ($d in @("css", "js")) {
    Copy-Item (Join-Path $WEBAPP $d) "$STAGE\assets\$d" -Recurse
}
# drop QA-only files and sw.js (assets are local inside the APK, a service worker is pointless)
Get-ChildItem "$STAGE\assets" -Recurse -File | Where-Object { $_.Name -like "_*" -or $_.Name -eq "sw.js" } | Remove-Item -Force
Write-Host ("assets files: " + (Get-ChildItem "$STAGE\assets" -Recurse -File | Measure-Object).Count)

# ---------- 2. compile resources ----------
Write-Host "=== 2. aapt2 compile ===" -ForegroundColor Cyan
& "$BT\aapt2.exe" compile --dir "$STAGE\res" -o "$STAGE\res.zip"
if ($LASTEXITCODE -ne 0) { throw "aapt2 compile failed" }

# ---------- 3. link (manifest + resources) ----------
Write-Host "=== 3. aapt2 link ===" -ForegroundColor Cyan
& "$BT\aapt2.exe" link `
    -o "$STAGE\app-unsigned.apk" `
    -I $PLATFORM `
    --manifest "$STAGE\AndroidManifest.xml" `
    --java "$STAGE\gen" `
    --min-sdk-version $MIN_SDK `
    --target-sdk-version $TARGET `
    --version-code $VER_CODE `
    --version-name $VER_NAME `
    --no-version-vectors `
    "$STAGE\res.zip"
if ($LASTEXITCODE -ne 0) { throw "aapt2 link failed" }

# ---------- 4. compile java ----------
Write-Host "=== 4. javac ===" -ForegroundColor Cyan
$sources = @(Get-ChildItem "$STAGE\java" -Recurse -Filter *.java | ForEach-Object { $_.FullName })
$gen = @(Get-ChildItem "$STAGE\gen" -Recurse -Filter *.java -ErrorAction SilentlyContinue | ForEach-Object { $_.FullName })
& "$JAVA_HOME\bin\javac.exe" -encoding UTF-8 -source 11 -target 11 -nowarn -cp $PLATFORM -d "$STAGE\classes" @($sources + $gen)
if ($LASTEXITCODE -ne 0) { throw "javac failed" }

# ---------- 5. dex ----------
Write-Host "=== 5. d8 ===" -ForegroundColor Cyan
& "$JAVA_HOME\bin\jar.exe" cf "$STAGE\classes.jar" -C "$STAGE\classes" .
& "$BT\d8.bat" --lib $PLATFORM --min-api $MIN_SDK --output "$STAGE\dex" "$STAGE\classes.jar"
if ($LASTEXITCODE -ne 0) { throw "d8 failed" }

# ---------- 6. put classes.dex + assets + the Go kernel into the apk ----------
# aapt2 -A writes backslash separators for nested files on Windows, which Android cannot resolve,
# so assets are injected here with proper forward slashes.
Write-Host "=== 6. pack dex + assets + kernel ===" -ForegroundColor Cyan
if (-not (Test-Path $KERNEL)) {
    throw "missing kernel: $KERNEL -- build it first: `$env:GOOS='linux';`$env:GOARCH='arm64';`$env:CGO_ENABLED='0'; go -C ..\core build -o bin\bzcore-android-arm64 ./cmd/bzcore"
}
Copy-Item "$STAGE\app-unsigned.apk" "$STAGE\app-dex.apk" -Force
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip = [System.IO.Compression.ZipFile]::Open("$STAGE\app-dex.apk", 'Update')
[System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, "$STAGE\dex\classes.dex", "classes.dex") | Out-Null
$assetsRoot = "$STAGE\assets"
$added = 0
Get-ChildItem $assetsRoot -Recurse -File | ForEach-Object {
    $rel = $_.FullName.Substring($assetsRoot.Length + 1).Replace('\', '/')
    [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, $_.FullName, "assets/$rel") | Out-Null
    $added++
}
# The kernel goes into lib/arm64-v8a/ as libbzcore.so: since Android 10 the app may only exec
# files the installer extracted from the APK's native lib dir (app data dir is blocked by W^X).
# Stored uncompressed so zipalign -p can page-align it.
$entry = $zip.CreateEntry("lib/arm64-v8a/libbzcore.so", [System.IO.Compression.CompressionLevel]::NoCompression)
$es = $entry.Open()
$kbytes = [System.IO.File]::ReadAllBytes($KERNEL)
$es.Write($kbytes, 0, $kbytes.Length)
$es.Close()
$zip.Dispose()
Write-Host ("packed entries: classes.dex + $added asset(s) + libbzcore.so (" + [math]::Round($kbytes.Length / 1MB, 2) + " MB)")

# ---------- 7. align ----------
Write-Host "=== 7. zipalign ===" -ForegroundColor Cyan
& "$BT\zipalign.exe" -p -f 4 "$STAGE\app-dex.apk" "$STAGE\app-aligned.apk"
if ($LASTEXITCODE -ne 0) { throw "zipalign failed" }

# ---------- 8. sign ----------
Write-Host "=== 8. sign ===" -ForegroundColor Cyan
# The keystore MUST live outside STAGE (which is wiped every build), otherwise the signature
# changes on every build and the APK can no longer be installed over the previous version.
$KS = Join-Path $SRC "debug.keystore"
if (-not (Test-Path -LiteralPath $KS)) {
    & "$JAVA_HOME\bin\keytool.exe" -genkeypair -keystore $KS -storepass android -keypass android `
        -alias androiddebugkey -keyalg RSA -keysize 2048 -validity 10000 `
        -dname "CN=Baize Debug, O=Baize, C=CN"
    if ($LASTEXITCODE -ne 0) { throw "keytool failed" }
}

$OUTAPK = Join-Path $SRC $OUTNAME
if (Test-Path -LiteralPath $OUTAPK) { Remove-Item -LiteralPath $OUTAPK -Force }
& "$BT\apksigner.bat" sign --ks $KS --ks-pass pass:android --key-pass pass:android `
    --min-sdk-version $MIN_SDK --out $OUTAPK "$STAGE\app-aligned.apk"
if ($LASTEXITCODE -ne 0) { throw "apksigner failed" }

# ---------- 9. verify ----------
Write-Host "=== 9. verify ===" -ForegroundColor Cyan
& "$BT\apksigner.bat" verify --print-certs $OUTAPK | Select-Object -First 3
& "$BT\aapt2.exe" dump badging $OUTAPK 2>$null | Select-String -Pattern "^package|^application-label|launchable-activity" | Select-Object -First 4

$size = [math]::Round((Get-Item -LiteralPath $OUTAPK).Length / 1MB, 2)
Write-Host ""
Write-Host "APK OK -> $OUTAPK ($size MB)" -ForegroundColor Green
