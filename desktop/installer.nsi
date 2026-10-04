; installer.nsi - the Baize desktop installer script (NSIS 3, Unicode).
;
; Built by desktop\build-installer.ps1. That script stages this file plus the
; compiled exe into an ASCII-only temp dir and passes VERSION / VERNUM / OUTFILE
; on the command line, so nothing here depends on the (non-ASCII) repo path.
;
; IMPORTANT: this file contains Chinese text and MUST be saved as UTF-8 with BOM,
; otherwise NSIS mis-decodes the labels. Keep the BOM.

Unicode true

!include "MUI2.nsh"
!include "FileFunc.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef VERNUM
  !define VERNUM "0.0.0.0"
!endif
!ifndef ROOTEXE
  !define ROOTEXE "${__FILEDIR__}\baize-desktop.exe"
!endif
!ifndef OUTFILE
  !define OUTFILE "${__FILEDIR__}\baize-desktop-setup.exe"
!endif
!ifndef ICON
  !define ICON "${__FILEDIR__}\icon.ico"
!endif

!define APPNAME   "白泽"
!define APPEXE    "baize-desktop.exe"
!define PUBLISHER "Baize"
!define REGCOMPANY "HDKL0802"
!define UNKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\BaizeDesktop"
!define RUNKEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define RUNVAL "BaizeDesktop"

Name "${APPNAME} ${VERSION}"
OutFile "${OUTFILE}"

; 安装包 / 卸载器 / 窗口都用同一个图标（assets/icon.ico）
Icon "${ICON}"
UninstallIcon "${ICON}"

; Per-user install: no UAC, no admin, and it lines up with the app writing its
; own autostart entry to HKCU (see autostart_windows.go).
InstallDir "$LOCALAPPDATA\Programs\Baize"
InstallDirRegKey HKCU "Software\Baize\Desktop" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show

VIProductVersion "${VERNUM}"
VIAddVersionKey /LANG=2052 "ProductName" "${APPNAME}"
VIAddVersionKey /LANG=2052 "FileDescription" "白泽桌面端安装程序"
VIAddVersionKey /LANG=2052 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "CompanyName" "${REGCOMPANY}"
VIAddVersionKey /LANG=2052 "LegalCopyright" "MIT License"

!define MUI_ABORTWARNING
!define MUI_ICON "${ICON}"
!define MUI_UNICON "${ICON}"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APPEXE}"
!define MUI_FINISHPAGE_RUN_TEXT "立即启动白泽"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Section "白泽桌面端（必需）" SEC_MAIN
  SectionIn RO
  SetOutPath "$INSTDIR"

  ; A running instance holds the exe open; auto-update may also have parked a
  ; ".old" rename behind. Clear both before laying down the new file.
  ExecWait '"$SYSDIR\taskkill.exe" /IM ${APPEXE} /F' $0
  Delete "$INSTDIR\${APPEXE}.old"
  File "${ROOTEXE}"
  Delete "$INSTDIR\${APPEXE}.old"

  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\${APPEXE}" "" "$INSTDIR\${APPEXE}" 0
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\${APPEXE}" "" "$INSTDIR\${APPEXE}" 0

  WriteUninstaller "$INSTDIR\uninstall.exe"

  WriteRegStr HKCU "Software\Baize\Desktop" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNKEY}" "DisplayName" "${APPNAME} (Baize)"
  WriteRegStr HKCU "${UNKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNKEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKCU "${UNKEY}" "DisplayIcon" "$INSTDIR\${APPEXE}"
  WriteRegStr HKCU "${UNKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKCU "${UNKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNKEY}" "EstimatedSize" "$0"
SectionEnd

Section "开机自动启动（推荐）" SEC_AUTO
  ; Same value shape the app itself writes ("<quoted exe path>"), so the
  ; settings toggle reads it back as enabled.
  WriteRegStr HKCU "${RUNKEY}" "${RUNVAL}" '"$INSTDIR\${APPEXE}"'
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SEC_MAIN} "安装白泽桌面端本体，并创建开始菜单与桌面快捷方式。"
  !insertmacro MUI_DESCRIPTION_TEXT ${SEC_AUTO} "登录 Windows 后自动在托盘中启动白泽，随时可连。可在设置里关闭。"
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "Uninstall"
  ExecWait '"$SYSDIR\taskkill.exe" /IM ${APPEXE} /F' $0
  DeleteRegValue HKCU "${RUNKEY}" "${RUNVAL}"
  DeleteRegKey HKCU "${UNKEY}"
  DeleteRegKey HKCU "Software\Baize\Desktop"
  Delete "$DESKTOP\${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"
  Delete "$INSTDIR\${APPEXE}"
  Delete "$INSTDIR\${APPEXE}.old"
  Delete "$INSTDIR\uninstall.exe"
  ; User data under %LOCALAPPDATA%\Baize (config / cache) is left alone.
  RMDir "$INSTDIR"
SectionEnd
