package fault

import (
	"os"
	"time"

	"github.com/spf13/afero"
)

// FS wraps base so that scheduled filesystem operations return an injected error
// instead of touching base. Both the Fs-level operations (Open, Create, Mkdir,
// Remove, Rename, Stat, ...) and reads/writes on the files it returns consult the
// schedule, so a harness can exercise both "the open failed" and the harder
// "the open succeeded but the read failed partway" branches. A nil/inactive
// schedule returns base unchanged, so production (which passes no schedule) gets
// the bare afero.Fs with zero overhead.
func FS(base afero.Fs, s *Schedule) afero.Fs {
	if !s.Active() {
		return base
	}
	return &faultFS{Fs: base, s: s}
}

// faultFS embeds afero.Fs so any method it does not override (Name, LstatIf
// possible, etc.) delegates unchanged. It overrides the fallible operations to
// consult the schedule first.
type faultFS struct {
	afero.Fs
	s *Schedule
}

func (f *faultFS) fault(op, name string) error {
	if err := f.s.trip(); err != nil {
		return &os.PathError{Op: op, Path: name, Err: err}
	}
	return nil
}

func (f *faultFS) Open(name string) (afero.File, error) {
	if err := f.fault("open", name); err != nil {
		return nil, err
	}
	file, err := f.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, s: f.s}, nil
}

func (f *faultFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if err := f.fault("open", name); err != nil {
		return nil, err
	}
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, s: f.s}, nil
}

func (f *faultFS) Create(name string) (afero.File, error) {
	if err := f.fault("create", name); err != nil {
		return nil, err
	}
	file, err := f.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, s: f.s}, nil
}

func (f *faultFS) Mkdir(name string, perm os.FileMode) error {
	if err := f.fault("mkdir", name); err != nil {
		return err
	}
	return f.Fs.Mkdir(name, perm)
}

func (f *faultFS) MkdirAll(path string, perm os.FileMode) error {
	if err := f.fault("mkdir", path); err != nil {
		return err
	}
	return f.Fs.MkdirAll(path, perm)
}

func (f *faultFS) Remove(name string) error {
	if err := f.fault("remove", name); err != nil {
		return err
	}
	return f.Fs.Remove(name)
}

func (f *faultFS) RemoveAll(path string) error {
	if err := f.fault("removeall", path); err != nil {
		return err
	}
	return f.Fs.RemoveAll(path)
}

func (f *faultFS) Rename(oldname, newname string) error {
	if err := f.fault("rename", oldname); err != nil {
		return err
	}
	return f.Fs.Rename(oldname, newname)
}

func (f *faultFS) Stat(name string) (os.FileInfo, error) {
	if err := f.fault("stat", name); err != nil {
		return nil, err
	}
	return f.Fs.Stat(name)
}

func (f *faultFS) Chmod(name string, mode os.FileMode) error {
	if err := f.fault("chmod", name); err != nil {
		return err
	}
	return f.Fs.Chmod(name, mode)
}

func (f *faultFS) Chtimes(name string, atime, mtime time.Time) error {
	if err := f.fault("chtimes", name); err != nil {
		return err
	}
	return f.Fs.Chtimes(name, atime, mtime)
}

// faultFile embeds afero.File so unoverridden methods delegate; Read/Write and
// their positional variants consult the schedule so a decode loop can hit an
// error partway through an otherwise-openable file.
type faultFile struct {
	afero.File
	s *Schedule
}

func (f *faultFile) Read(p []byte) (int, error) {
	if err := f.s.trip(); err != nil {
		return 0, err
	}
	return f.File.Read(p)
}

func (f *faultFile) ReadAt(p []byte, off int64) (int, error) {
	if err := f.s.trip(); err != nil {
		return 0, err
	}
	return f.File.ReadAt(p, off)
}

func (f *faultFile) Write(p []byte) (int, error) {
	if err := f.s.trip(); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}

func (f *faultFile) WriteAt(p []byte, off int64) (int, error) {
	if err := f.s.trip(); err != nil {
		return 0, err
	}
	return f.File.WriteAt(p, off)
}
