package app

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// copyBackupTree stages the whole stopped server data directory without
// following symbolic links. File modes and modification times are preserved.
func copyBackupTree(src, dst string) error {
	type directory struct {
		path  string
		mode  fs.FileMode
		mtime int64
	}
	dirs := []directory{}
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		mode := info.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, mode.Perm()|0700); err != nil {
				return err
			}
			dirs = append(dirs, directory{target, mode.Perm(), info.ModTime().UnixNano()})
		case mode.IsRegular():
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
			if err != nil {
				_ = in.Close()
				return err
			}
			_, copyErr := io.Copy(out, in)
			inErr := in.Close()
			outErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if inErr != nil {
				return inErr
			}
			if outErr != nil {
				return outErr
			}
			if err := os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		default:
			return errors.New("Server data contains a special file that cannot be backed up")
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		if err := os.Chmod(d.path, d.mode); err != nil {
			return err
		}
		stamp := time.Unix(0, d.mtime)
		if err := os.Chtimes(d.path, stamp, stamp); err != nil {
			return err
		}
	}
	return nil
}
