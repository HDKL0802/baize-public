package com.baize.todo;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.database.Cursor;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.webkit.MimeTypeMap;

import java.io.File;

/**
 * 待办附件的对外通道。
 *
 * 附件存在 App 私有目录（/data/data/com.baize.todo/files/att/），私有目录里的文件
 * 不能直接用 file:// 交给别的应用（Android 7 起会抛 FileUriExposedException），
 * 而工程里没有 AndroidX 的 FileProvider，所以这里自己做一个最小实现：
 * 只允许读取 att/ 目录下的文件，由调用方（MainActivity）用
 * FLAG_GRANT_READ_URI_PERMISSION 临时授权给相册 / WPS 之类的应用。
 */
public class AttProvider extends ContentProvider {

    public static final String AUTHORITY = "com.baize.todo.att";

    /** 附件目录名（MainActivity 也用这个） */
    public static final String DIR = "att";

    public static Uri uriFor(File f) {
        return Uri.parse("content://" + AUTHORITY + "/" + f.getName());
    }

    @Override
    public boolean onCreate() {
        return true;
    }

    /** 只解析 att/ 目录下的单层文件名，杜绝 ../ 之类的越权读取 */
    private File resolve(Uri uri) {
        String name = uri.getLastPathSegment();
        if (name == null || name.isEmpty() || name.contains("..") || name.indexOf('/') >= 0
                || name.indexOf('\\') >= 0) {
            return null;
        }
        if (getContext() == null) return null;
        return new File(new File(getContext().getFilesDir(), DIR), name);
    }

    @Override
    public ParcelFileDescriptor openFile(Uri uri, String mode) throws java.io.FileNotFoundException {
        File f = resolve(uri);
        if (f == null || !f.isFile()) throw new java.io.FileNotFoundException(String.valueOf(uri));
        return ParcelFileDescriptor.open(f, ParcelFileDescriptor.MODE_READ_ONLY);
    }

    @Override
    public String getType(Uri uri) {
        File f = resolve(uri);
        if (f == null) return "application/octet-stream";
        String n = f.getName();
        int dot = n.lastIndexOf('.');
        String ext = dot > 0 ? n.substring(dot + 1).toLowerCase() : "";
        String m = MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext);
        return m == null ? "application/octet-stream" : m;
    }

    @Override
    public Cursor query(Uri uri, String[] projection, String selection, String[] selectionArgs, String sortOrder) {
        return null;
    }

    @Override
    public Uri insert(Uri uri, ContentValues values) {
        return null;
    }

    @Override
    public int delete(Uri uri, String selection, String[] selectionArgs) {
        return 0;
    }

    @Override
    public int update(Uri uri, ContentValues values, String selection, String[] selectionArgs) {
        return 0;
    }
}
