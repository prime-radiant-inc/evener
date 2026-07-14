package identifier

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// Project is the stable identity of a canonical project directory.
type Project struct {
	ID            string
	CanonicalPath string
}

// Resolver is the filesystem and Git seam used by project resolution.
type Resolver interface {
	Abs(string) (string, error)
	EvalSymlinks(string) (string, error)
	MainCheckout(string) (string, bool, error)
}

type localResolver struct{}

func (localResolver) Abs(path string) (string, error) { return filepath.Abs(path) }
func (localResolver) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
func (localResolver) MainCheckout(path string) (string, bool, error) {
	return mainCheckoutLocal(path)
}

var (
	errEmptyProjectPath = errors.New("project path is empty")
	errNilResolver      = errors.New("project resolver is nil")
)

// ResolveProject resolves path using the local filesystem and Git.
func ResolveProject(path string) (Project, error) {
	return ResolveProjectWith(path, localResolver{})
}

// ResolveProjectWith resolves path through resolver, canonicalizing both the
// active path and (for Git projects) the selected main checkout.
func ResolveProjectWith(path string, resolver Resolver) (Project, error) {
	if path == "" {
		return Project{}, errEmptyProjectPath
	}
	if isNilResolver(resolver) {
		return Project{}, errNilResolver
	}

	absolute, err := resolver.Abs(path)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project absolute path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	canonical, err := resolver.EvalSymlinks(absolute)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project symlinks: %w", err)
	}
	canonical = filepath.Clean(canonical)
	if err := existingDirectory(canonical); err != nil {
		return Project{}, fmt.Errorf("resolve project path: %w", err)
	}

	selected, isGit, err := resolver.MainCheckout(canonical)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project Git checkout: %w", err)
	}
	if isGit {
		if strings.TrimSpace(selected) == "" {
			return Project{}, errors.New("resolve project Git checkout: empty main checkout root")
		}
		canonical = filepath.Clean(selected)
	}
	canonical, err = resolver.EvalSymlinks(canonical)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project identity symlinks: %w", err)
	}
	canonical = filepath.Clean(canonical)
	if err := existingDirectory(canonical); err != nil {
		return Project{}, fmt.Errorf("resolve project identity path: %w", err)
	}
	return Project{ID: projectID(canonical), CanonicalPath: canonical}, nil
}

func isNilResolver(resolver Resolver) bool {
	if resolver == nil {
		return true
	}
	value := reflect.ValueOf(resolver)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func existingDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	return nil
}

// ProjectID resolves path before rendering its identifier.
func ProjectID(path string) (string, error) {
	project, err := ResolveProject(path)
	if err != nil {
		return "", err
	}
	return project.ID, nil
}

// ValidateProjectID validates only the serialized Project ID syntax.
func ValidateProjectID(value string) error {
	if len(value) == 0 || len(value) > 80 {
		return errors.New("project ID must be 1..80 ASCII bytes")
	}
	for i := 0; i < len(value); i++ {
		if !isASCIIAlphaNumeric(value[i]) && value[i] != '-' {
			return errors.New("project ID contains invalid character")
		}
	}
	separator := strings.LastIndexByte(value, '-')
	if separator <= 0 || len(value)-separator-1 != 10 {
		return errors.New("project ID must have a readable portion and 10-character suffix")
	}
	for _, b := range []byte(value[separator+1:]) {
		if !isBase62(b) {
			return errors.New("project ID suffix is not base62")
		}
	}
	return nil
}

func isASCIIAlphaNumeric(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
func isBase62(b byte) bool { return isASCIIAlphaNumeric(b) }

func projectID(canonicalPath string) string {
	const suffixWidth = 10
	const maxIDBytes = 80
	readable := readableProjectPath(canonicalPath)
	suffix := projectSuffix(canonicalPath)
	maxReadable := maxIDBytes - 1 - suffixWidth
	if len(readable) > maxReadable {
		readable = strings.TrimLeft(readable[len(readable)-maxReadable:], "-")
	}
	if readable == "" {
		readable = "project"
	}
	return readable + "-" + suffix
}

func readableProjectPath(path string) string {
	path = filepath.Clean(path)
	volume := filepath.VolumeName(path)
	path = strings.TrimPrefix(path, volume)
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	readable := make([]string, 0, len(parts))
	for _, part := range parts {
		var b strings.Builder
		lastHyphen := false
		for i := 0; i < len(part); i++ {
			c := part[i]
			if isASCIIAlphaNumeric(c) {
				b.WriteByte(c)
				lastHyphen = false
			} else if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
		part = strings.Trim(b.String(), "-")
		if part != "" {
			readable = append(readable, part)
		}
	}
	if len(readable) == 0 {
		return "project"
	}
	return strings.Join(readable, "-")
}

func projectSuffix(path string) string {
	return base62Digest(path, 10)
}

func base62Digest(path string, width int) string {
	digest := sha256.Sum256([]byte(path))
	n := new(big.Int).SetBytes(digest[:])
	modulus := new(big.Int).Exp(big.NewInt(62), big.NewInt(int64(width)), nil)
	n.Mod(n, modulus)
	result := make([]byte, width)
	base := big.NewInt(62)
	for i := len(result) - 1; i >= 0; i-- {
		var remainder big.Int
		n.QuoRem(n, base, &remainder)
		result[i] = base62Alphabet[remainder.Int64()]
	}
	return string(result)
}
