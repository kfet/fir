package resources

import (
	_ "embed"

	"github.com/kfet/fir/pkg/zfs"
)

//go:generate go run ../../cmd/zfsgen -root . -o builtin.tar.gz builtin_skills builtin_extensions

//go:embed builtin.tar.gz
var builtinBlob []byte

// builtinFS holds the compressed builtin skills and extensions.
var builtinFS = zfs.New(builtinBlob)

// BuiltinSkillsFS serves "builtin_skills/..." paths.
var BuiltinSkillsFS = builtinFS

// BuiltinExtensionsFS serves "builtin_extensions/..." paths.
var BuiltinExtensionsFS = builtinFS
