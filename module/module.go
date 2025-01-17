// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package module defines the module.Version type along with support code.
//
// The module.Version type is a simple Path, Version pair:
//
//	type Version struct {
//		Path string
//		Version string
//	}
//
// There are no restrictions imposed directly by use of this structure,
// but additional checking functions, most notably Check, verify that
// a particular path, version pair is valid.
//
// Escaped Paths
//
// Module paths appear as substrings of file system paths
// (in the download cache) and of web server URLs in the proxy protocol.
// In general we cannot rely on file systems to be case-sensitive,
// nor can we rely on web servers, since they read from file systems.
// That is, we cannot rely on the file system to keep rsc.io/QUOTE
// and rsc.io/quote separate. Windows and macOS don't.
// Instead, we must never require two different casings of a file path.
// Because we want the download cache to match the proxy protocol,
// and because we want the proxy protocol to be possible to serve
// from a tree of static files (which might be stored on a case-insensitive
// file system), the proxy protocol must never require two different casings
// of a URL path either.
//
// One possibility would be to make the escaped form be the lowercase
// hexadecimal encoding of the actual path bytes. This would avoid ever
// needing different casings of a file path, but it would be fairly illegible
// to most programmers when those paths appeared in the file system
// (including in file paths in compiler errors and stack traces)
// in web server logs, and so on. Instead, we want a safe escaped form that
// leaves most paths unaltered.
//
// The safe escaped form is to replace every uppercase letter
// with an exclamation mark followed by the letter's lowercase equivalent.
//
// For example,
//
//	github.com/Azure/azure-sdk-for-go ->  github.com/!azure/azure-sdk-for-go.
//	github.com/GoogleCloudPlatform/cloudsql-proxy -> github.com/!google!cloud!platform/cloudsql-proxy
//	github.com/Sirupsen/logrus -> github.com/!sirupsen/logrus.
//
// Import paths that avoid upper-case letters are left unchanged.
// Note that because import paths are ASCII-only and avoid various
// problematic punctuation (like : < and >), the escaped form is also ASCII-only
// and avoids the same problematic punctuation.
//
// Import paths have never allowed exclamation marks, so there is no
// need to define how to escape a literal !.
//
// Unicode Restrictions
//
// Today, paths are disallowed from using Unicode.
//
// Although paths are currently disallowed from using Unicode,
// we would like at some point to allow Unicode letters as well, to assume that
// file systems and URLs are Unicode-safe (storing UTF-8), and apply
// the !-for-uppercase convention for escaping them in the file system.
// But there are at least two subtle considerations.
//
// First, note that not all case-fold equivalent distinct runes
// form an upper/lower pair.
// For example, U+004B ('K'), U+006B ('k'), and U+212A ('K' for Kelvin)
// are three distinct runes that case-fold to each other.
// When we do add Unicode letters, we must not assume that upper/lower
// are the only case-equivalent pairs.
// Perhaps the Kelvin symbol would be disallowed entirely, for example.
// Or perhaps it would escape as "!!k", or perhaps as "(212A)".
//
// Second, it would be nice to allow Unicode marks as well as letters,
// but marks include combining marks, and then we must deal not
// only with case folding but also normalization: both U+00E9 ('é')
// and U+0065 U+0301 ('e' followed by combining acute accent)
// look the same on the page and are treated by some file systems
// as the same path. If we do allow Unicode marks in paths, there
// must be some kind of normalization to allow only one canonical
// encoding of any character used in an import path.
package module

// IMPORTANT NOTE
//
// This file essentially defines the set of valid import paths for the go command.
// There are many subtle considerations, including Unicode ambiguity,
// security, network, and file system representations.
//
// This file also defines the set of valid module path and version combinations,
// another topic with many subtle considerations.
//
// Changes to the semantics in this file require approval from rsc.

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/radeksimko/mod/semver"
)

// A Version (for clients, a module.Version) is defined by a module path and version pair.
// These are stored in their plain (unescaped) form.
type Version struct {
	// Path is a module path, like "golang.org/x/text" or "rsc.io/quote/v2".
	Path string

	// Version is a module version.
	// By convention, the version string is in canonical semver form,
	// but enforcement is left up to individual APIs.
	Version string `json:",omitempty"`
}

// String returns the module version syntax Path@Version.
func (m Version) String() string {
	return m.Path + "@" + m.Version
}

// Check checks that a given module path, version pair is valid.
// In addition to the path being a valid module path
// and the version being a valid semantic version,
// the two must correspond.
// For example, the path "yaml/v2" only corresponds to
// semantic versions beginning with "v2.".
func Check(path, version string) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	if !semver.IsValid(version) {
		return fmt.Errorf("malformed semantic version %v", version)
	}
	_, pathMajor, _ := SplitPathVersion(path)
	if !MatchPathMajor(version, pathMajor) {
		if pathMajor == "" {
			pathMajor = "v0 or v1"
		}
		if pathMajor[0] == '.' { // .v1
			pathMajor = pathMajor[1:]
		}
		return fmt.Errorf("mismatched module path %v and version %v (want %v)", path, version, pathMajor)
	}
	return nil
}

// firstPathOK reports whether r can appear in the first element of a module path.
// The first element of the path must be an LDH domain name, at least for now.
// To avoid case ambiguity, the domain name must be entirely lower case.
func firstPathOK(r rune) bool {
	return r == '-' || r == '.' ||
		'0' <= r && r <= '9' ||
		'a' <= r && r <= 'z'
}

// pathOK reports whether r can appear in an import path element.
// Paths can be ASCII letters, ASCII digits, and limited ASCII punctuation: + - . _ and ~.
// This matches what "go get" has historically recognized in import paths.
// TODO(rsc): We would like to allow Unicode letters, but that requires additional
// care in the safe encoding (see note above).
func pathOK(r rune) bool {
	if r < utf8.RuneSelf {
		return r == '+' || r == '-' || r == '.' || r == '_' || r == '~' ||
			'0' <= r && r <= '9' ||
			'A' <= r && r <= 'Z' ||
			'a' <= r && r <= 'z'
	}
	return false
}

// fileNameOK reports whether r can appear in a file name.
// For now we allow all Unicode letters but otherwise limit to pathOK plus a few more punctuation characters.
// If we expand the set of allowed characters here, we have to
// work harder at detecting potential case-folding and normalization collisions.
// See note about "safe encoding" above.
func fileNameOK(r rune) bool {
	if r < utf8.RuneSelf {
		// Entire set of ASCII punctuation, from which we remove characters:
		//     ! " # $ % & ' ( ) * + , - . / : ; < = > ? @ [ \ ] ^ _ ` { | } ~
		// We disallow some shell special characters: " ' * < > ? ` |
		// (Note that some of those are disallowed by the Windows file system as well.)
		// We also disallow path separators / : and \ (fileNameOK is only called on path element characters).
		// We allow spaces (U+0020) in file names.
		const allowed = "!#$%&()+,-.=@[]^_{}~ "
		if '0' <= r && r <= '9' || 'A' <= r && r <= 'Z' || 'a' <= r && r <= 'z' {
			return true
		}
		for i := 0; i < len(allowed); i++ {
			if rune(allowed[i]) == r {
				return true
			}
		}
		return false
	}
	// It may be OK to add more ASCII punctuation here, but only carefully.
	// For example Windows disallows < > \, and macOS disallows :, so we must not allow those.
	return unicode.IsLetter(r)
}

// CheckPath checks that a module path is valid.
// A valid module path is a valid import path, as checked by CheckImportPath,
// with two additional constraints.
// First, the leading path element (up to the first slash, if any),
// by convention a domain name, must contain only lower-case ASCII letters,
// ASCII digits, dots (U+002E), and dashes (U+002D);
// it must contain at least one dot and cannot start with a dash.
// Second, for a final path element of the form /vN, where N looks numeric
// (ASCII digits and dots) must not begin with a leading zero, must not be /v1,
// and must not contain any dots. For paths beginning with "gopkg.in/",
// this second requirement is replaced by a requirement that the path
// follow the gopkg.in server's conventions.
func CheckPath(path string) error {
	if err := checkPath(path, false); err != nil {
		return fmt.Errorf("malformed module path %q: %v", path, err)
	}
	i := strings.Index(path, "/")
	if i < 0 {
		i = len(path)
	}
	if i == 0 {
		return fmt.Errorf("malformed module path %q: leading slash", path)
	}
	if !strings.Contains(path[:i], ".") {
		return fmt.Errorf("malformed module path %q: missing dot in first path element", path)
	}
	if path[0] == '-' {
		return fmt.Errorf("malformed module path %q: leading dash in first path element", path)
	}
	for _, r := range path[:i] {
		if !firstPathOK(r) {
			return fmt.Errorf("malformed module path %q: invalid char %q in first path element", path, r)
		}
	}
	if _, _, ok := SplitPathVersion(path); !ok {
		return fmt.Errorf("malformed module path %q: invalid version", path)
	}
	return nil
}

// CheckImportPath checks that an import path is valid.
//
// A valid import path consists of one or more valid path elements
// separated by slashes (U+002F). (It must not begin with nor end in a slash.)
//
// A valid path element is a non-empty string made up of
// ASCII letters, ASCII digits, and limited ASCII punctuation: + - . _ and ~.
// It must not begin or end with a dot (U+002E), nor contain two dots in a row.
//
// The element prefix up to the first dot must not be a reserved file name
// on Windows, regardless of case (CON, com1, NuL, and so on).
//
// CheckImportPath may be less restrictive in the future, but see the
// top-level package documentation for additional information about
// subtleties of Unicode.
func CheckImportPath(path string) error {
	if err := checkPath(path, false); err != nil {
		return fmt.Errorf("malformed import path %q: %v", path, err)
	}
	return nil
}

// checkPath checks that a general path is valid.
// It returns an error describing why but not mentioning path.
// Because these checks apply to both module paths and import paths,
// the caller is expected to add the "malformed ___ path %q: " prefix.
// fileName indicates whether the final element of the path is a file name
// (as opposed to a directory name).
func checkPath(path string, fileName bool) error {
	if !utf8.ValidString(path) {
		return fmt.Errorf("invalid UTF-8")
	}
	if path == "" {
		return fmt.Errorf("empty string")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("double dot")
	}
	if strings.Contains(path, "//") {
		return fmt.Errorf("double slash")
	}
	if path[len(path)-1] == '/' {
		return fmt.Errorf("trailing slash")
	}
	elemStart := 0
	for i, r := range path {
		if r == '/' {
			if err := checkElem(path[elemStart:i], fileName); err != nil {
				return err
			}
			elemStart = i + 1
		}
	}
	if err := checkElem(path[elemStart:], fileName); err != nil {
		return err
	}
	return nil
}

// checkElem checks whether an individual path element is valid.
// fileName indicates whether the element is a file name (not a directory name).
func checkElem(elem string, fileName bool) error {
	if elem == "" {
		return fmt.Errorf("empty path element")
	}
	if strings.Count(elem, ".") == len(elem) {
		return fmt.Errorf("invalid path element %q", elem)
	}
	if elem[0] == '.' && !fileName {
		return fmt.Errorf("leading dot in path element")
	}
	if elem[len(elem)-1] == '.' {
		return fmt.Errorf("trailing dot in path element")
	}
	charOK := pathOK
	if fileName {
		charOK = fileNameOK
	}
	for _, r := range elem {
		if !charOK(r) {
			return fmt.Errorf("invalid char %q", r)
		}
	}

	// Windows disallows a bunch of path elements, sadly.
	// See https://docs.microsoft.com/en-us/windows/desktop/fileio/naming-a-file
	short := elem
	if i := strings.Index(short, "."); i >= 0 {
		short = short[:i]
	}
	for _, bad := range badWindowsNames {
		if strings.EqualFold(bad, short) {
			return fmt.Errorf("%q disallowed as path element component on Windows", short)
		}
	}
	return nil
}

// CheckFilePath checks that a slash-separated file path is valid.
// The definition of a valid file path is the same as the definition
// of a valid import path except that the set of allowed characters is larger:
// all Unicode letters, ASCII digits, the ASCII space character (U+0020),
// and the ASCII punctuation characters
// “!#$%&()+,-.=@[]^_{}~”.
// (The excluded punctuation characters, " * < > ? ` ' | / \ and :,
// have special meanings in certain shells or operating systems.)
//
// CheckFilePath may be less restrictive in the future, but see the
// top-level package documentation for additional information about
// subtleties of Unicode.
func CheckFilePath(path string) error {
	if err := checkPath(path, true); err != nil {
		return fmt.Errorf("malformed file path %q: %v", path, err)
	}
	return nil
}

// badWindowsNames are the reserved file path elements on Windows.
// See https://docs.microsoft.com/en-us/windows/desktop/fileio/naming-a-file
var badWindowsNames = []string{
	"CON",
	"PRN",
	"AUX",
	"NUL",
	"COM1",
	"COM2",
	"COM3",
	"COM4",
	"COM5",
	"COM6",
	"COM7",
	"COM8",
	"COM9",
	"LPT1",
	"LPT2",
	"LPT3",
	"LPT4",
	"LPT5",
	"LPT6",
	"LPT7",
	"LPT8",
	"LPT9",
}

// SplitPathVersion returns prefix and major version such that prefix+pathMajor == path
// and version is either empty or "/vN" for N >= 2.
// As a special case, gopkg.in paths are recognized directly;
// they require ".vN" instead of "/vN", and for all N, not just N >= 2.
// SplitPathVersion returns with ok = false when presented with
// a path whose last path element does not satisfy the constraints
// applied by CheckPath, such as "example.com/pkg/v1" or "example.com/pkg/v1.2".
func SplitPathVersion(path string) (prefix, pathMajor string, ok bool) {
	if strings.HasPrefix(path, "gopkg.in/") {
		return splitGopkgIn(path)
	}

	i := len(path)
	dot := false
	for i > 0 && ('0' <= path[i-1] && path[i-1] <= '9' || path[i-1] == '.') {
		if path[i-1] == '.' {
			dot = true
		}
		i--
	}
	if i <= 1 || i == len(path) || path[i-1] != 'v' || path[i-2] != '/' {
		return path, "", true
	}
	prefix, pathMajor = path[:i-2], path[i-2:]
	if dot || len(pathMajor) <= 2 || pathMajor[2] == '0' || pathMajor == "/v1" {
		return path, "", false
	}
	return prefix, pathMajor, true
}

// splitGopkgIn is like SplitPathVersion but only for gopkg.in paths.
func splitGopkgIn(path string) (prefix, pathMajor string, ok bool) {
	if !strings.HasPrefix(path, "gopkg.in/") {
		return path, "", false
	}
	i := len(path)
	if strings.HasSuffix(path, "-unstable") {
		i -= len("-unstable")
	}
	for i > 0 && ('0' <= path[i-1] && path[i-1] <= '9') {
		i--
	}
	if i <= 1 || path[i-1] != 'v' || path[i-2] != '.' {
		// All gopkg.in paths must end in vN for some N.
		return path, "", false
	}
	prefix, pathMajor = path[:i-2], path[i-2:]
	if len(pathMajor) <= 2 || pathMajor[2] == '0' && pathMajor != ".v0" {
		return path, "", false
	}
	return prefix, pathMajor, true
}

// MatchPathMajor reports whether the semantic version v
// matches the path major version pathMajor.
func MatchPathMajor(v, pathMajor string) bool {
	if strings.HasPrefix(pathMajor, ".v") && strings.HasSuffix(pathMajor, "-unstable") {
		pathMajor = strings.TrimSuffix(pathMajor, "-unstable")
	}
	if strings.HasPrefix(v, "v0.0.0-") && pathMajor == ".v1" {
		// Allow old bug in pseudo-versions that generated v0.0.0- pseudoversion for gopkg .v1.
		// For example, gopkg.in/yaml.v2@v2.2.1's go.mod requires gopkg.in/check.v1 v0.0.0-20161208181325-20d25e280405.
		return true
	}
	m := semver.Major(v)
	if pathMajor == "" {
		return m == "v0" || m == "v1" || semver.Build(v) == "+incompatible"
	}
	return (pathMajor[0] == '/' || pathMajor[0] == '.') && m == pathMajor[1:]
}

// CanonicalVersion returns the canonical form of the version string v.
// It is the same as semver.Canonical(v) except that it preserves the special build suffix "+incompatible".
func CanonicalVersion(v string) string {
	cv := semver.Canonical(v)
	if semver.Build(v) == "+incompatible" {
		cv += "+incompatible"
	}
	return cv
}

// Sort sorts the list by Path, breaking ties by comparing Version fields.
// The Version fields are interpreted as semantic versions (using semver.Compare)
// optionally followed by a tie-breaking suffix introduced by a slash character,
// like in "v0.0.1/go.mod".
func Sort(list []Version) {
	sort.Slice(list, func(i, j int) bool {
		mi := list[i]
		mj := list[j]
		if mi.Path != mj.Path {
			return mi.Path < mj.Path
		}
		// To help go.sum formatting, allow version/file.
		// Compare semver prefix by semver rules,
		// file by string order.
		vi := mi.Version
		vj := mj.Version
		var fi, fj string
		if k := strings.Index(vi, "/"); k >= 0 {
			vi, fi = vi[:k], vi[k:]
		}
		if k := strings.Index(vj, "/"); k >= 0 {
			vj, fj = vj[:k], vj[k:]
		}
		if vi != vj {
			return semver.Compare(vi, vj) < 0
		}
		return fi < fj
	})
}

// EscapePath returns the escaped form of the given module path.
// It fails if the module path is invalid.
func EscapePath(path string) (escaped string, err error) {
	if err := CheckPath(path); err != nil {
		return "", err
	}

	return escapeString(path)
}

// EscapeVersion returns the escaped form of the given module version.
// Versions are allowed to be in non-semver form but must be valid file names
// and not contain exclamation marks.
func EscapeVersion(v string) (escaped string, err error) {
	if err := checkElem(v, true); err != nil || strings.Contains(v, "!") {
		return "", fmt.Errorf("disallowed version string %q", v)
	}
	return escapeString(v)
}

func escapeString(s string) (escaped string, err error) {
	haveUpper := false
	for _, r := range s {
		if r == '!' || r >= utf8.RuneSelf {
			// This should be disallowed by CheckPath, but diagnose anyway.
			// The correctness of the escaping loop below depends on it.
			return "", fmt.Errorf("internal error: inconsistency in EscapePath")
		}
		if 'A' <= r && r <= 'Z' {
			haveUpper = true
		}
	}

	if !haveUpper {
		return s, nil
	}

	var buf []byte
	for _, r := range s {
		if 'A' <= r && r <= 'Z' {
			buf = append(buf, '!', byte(r+'a'-'A'))
		} else {
			buf = append(buf, byte(r))
		}
	}
	return string(buf), nil
}

// UnescapePath returns the module path for the given escaped path.
// It fails if the escaped path is invalid or describes an invalid path.
func UnescapePath(escaped string) (path string, err error) {
	path, ok := unescapeString(escaped)
	if !ok {
		return "", fmt.Errorf("invalid escaped module path %q", escaped)
	}
	if err := CheckPath(path); err != nil {
		return "", fmt.Errorf("invalid escaped module path %q: %v", escaped, err)
	}
	return path, nil
}

// UnescapeVersion returns the version string for the given escaped version.
// It fails if the escaped form is invalid or describes an invalid version.
// Versions are allowed to be in non-semver form but must be valid file names
// and not contain exclamation marks.
func UnescapeVersion(escaped string) (v string, err error) {
	v, ok := unescapeString(escaped)
	if !ok {
		return "", fmt.Errorf("invalid escaped version %q", escaped)
	}
	if err := checkElem(v, true); err != nil {
		return "", fmt.Errorf("invalid escaped version %q: %v", v, err)
	}
	return v, nil
}

func unescapeString(escaped string) (string, bool) {
	var buf []byte

	bang := false
	for _, r := range escaped {
		if r >= utf8.RuneSelf {
			return "", false
		}
		if bang {
			bang = false
			if r < 'a' || 'z' < r {
				return "", false
			}
			buf = append(buf, byte(r+'A'-'a'))
			continue
		}
		if r == '!' {
			bang = true
			continue
		}
		if 'A' <= r && r <= 'Z' {
			return "", false
		}
		buf = append(buf, byte(r))
	}
	if bang {
		return "", false
	}
	return string(buf), true
}
