package chezmoi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

var errUnknownFormat = errors.New("unknown format")

// An walkArchiveFunc is called once for each entry in an archive.
type walkArchiveFunc func(name string, info fs.FileInfo, r io.Reader, linkname string) error

// A ArchiveReaderSystem a system constructed from reading an archive.
type ArchiveReaderSystem struct {
	emptySystemMixin
	noUpdateSystemMixin
	fileInfos map[AbsPath]fs.FileInfo
	contents  map[AbsPath][]byte
	linkname  map[AbsPath]string
}

// ArchiveReaderSystemOptions are options to NewArchiveReaderSystem.
type ArchiveReaderSystemOptions struct {
	RootAbsPath     AbsPath
	StripComponents int
}

// NewArchiveReaderSystem returns a new ArchiveReaderSystem reading from data
// and using path as a hint for the archive format.
func NewArchiveReaderSystem(path string, data []byte, options ArchiveReaderSystemOptions) (*ArchiveReaderSystem, error) {
	s := &ArchiveReaderSystem{
		fileInfos: make(map[AbsPath]fs.FileInfo),
		contents:  make(map[AbsPath][]byte),
		linkname:  make(map[AbsPath]string),
	}

	if err := walkArchive(path, data, func(name string, info fs.FileInfo, r io.Reader, linkname string) error {
		if options.StripComponents > 0 {
			components := strings.Split(name, "/")
			if len(components) <= options.StripComponents {
				return nil
			}
			name = strings.Join(components[options.StripComponents:], "/")
		}
		if name == "" {
			return nil
		}
		nameAbsPath := options.RootAbsPath.Join(RelPath(name))

		s.fileInfos[nameAbsPath] = info
		switch {
		case info.IsDir():
		case info.Mode()&fs.ModeType == 0:
			contents, err := io.ReadAll(r)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			s.contents[nameAbsPath] = contents
		case info.Mode()&fs.ModeType == fs.ModeSymlink:
			s.linkname[nameAbsPath] = linkname
		default:
			return fmt.Errorf("%s: unsupported mode %o", name, info.Mode()&fs.ModeType)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return s, nil
}

// FileInfos returns s's fs.FileInfos.
func (s *ArchiveReaderSystem) FileInfos() map[AbsPath]fs.FileInfo {
	return s.fileInfos
}

// Lstat implements System.Lstat.
func (s *ArchiveReaderSystem) Lstat(filename AbsPath) (fs.FileInfo, error) {
	fileInfo, ok := s.fileInfos[filename]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return fileInfo, nil
}

// ReadFile implements System.ReadFile.
func (s *ArchiveReaderSystem) ReadFile(name AbsPath) ([]byte, error) {
	if contents, ok := s.contents[name]; ok {
		return contents, nil
	}
	if _, ok := s.fileInfos[name]; ok {
		return nil, fs.ErrInvalid
	}
	return nil, fs.ErrNotExist
}

// Readlink implements System.Readlink.
func (s *ArchiveReaderSystem) Readlink(name AbsPath) (string, error) {
	if linkname, ok := s.linkname[name]; ok {
		return linkname, nil
	}
	if _, ok := s.fileInfos[name]; ok {
		return "", fs.ErrInvalid
	}
	return "", fs.ErrNotExist
}

// walkArchive walks over all the entries in an archive. path is used as a hint
// for the archive format.
func walkArchive(path string, data []byte, f walkArchiveFunc) error {
	pathLower := strings.ToLower(path)
	if strings.HasSuffix(pathLower, ".zip") {
		return walkArchiveZip(bytes.NewReader(data), int64(len(data)), f)
	}
	var r io.Reader = bytes.NewReader(data)
	switch {
	case strings.HasSuffix(pathLower, ".tar"):
	case strings.HasSuffix(pathLower, ".tar.bz2") || strings.HasSuffix(pathLower, ".tbz2"):
		r = bzip2.NewReader(r)
	case strings.HasSuffix(pathLower, ".tar.gz") || strings.HasSuffix(pathLower, ".tgz"):
		var err error
		r, err = gzip.NewReader(r)
		if err != nil {
			return err
		}
	default:
		return errUnknownFormat
	}
	return walkArchiveTar(r, f)
}

// walkArchiveTar walks over all the entries in a tar archive.
func walkArchiveTar(r io.Reader, f walkArchiveFunc) error {
	tarReader := tar.NewReader(r)
	for {
		header, err := tarReader.Next()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		switch header.Typeflag {
		case tar.TypeDir, tar.TypeReg:
			if err := f(name, header.FileInfo(), tarReader, ""); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := f(name, header.FileInfo(), nil, header.Linkname); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("%s: unsupported typeflag '%c'", header.Name, header.Typeflag)
		}
	}
}

// walkArchiveZip walks over all the entries in a zip archive.
func walkArchiveZip(r io.ReaderAt, size int64, f walkArchiveFunc) error {
	zipReader, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	for _, zipFile := range zipReader.File {
		zipFileReader, err := zipFile.Open()
		if err != nil {
			return err
		}
		name := path.Clean(zipFile.Name)
		if strings.HasPrefix(name, "../") {
			return fmt.Errorf("%s: invalid filename", zipFile.Name)
		}
		err = f(name, zipFile.FileInfo(), zipFileReader, "")
		zipFileReader.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
