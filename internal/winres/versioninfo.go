package winres

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"unicode/utf16"
)

// Info is the VERSIONINFO identity stamped onto one executable.
type Info struct {
	InternalName     string
	OriginalFilename string
	FileDescription  string
	Release          string
	Company          string
	Product          string
	Copyright        string
	IncludeMessages  bool
}

var releasePattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z.-]+)?$`)

type fileVersion struct {
	Major, Minor, Patch, Build uint16
	Numeric                    string
	Product                    string
}

func parseRelease(release string) (fileVersion, error) {
	m := releasePattern.FindStringSubmatch(release)
	if m == nil {
		return fileVersion{}, fmt.Errorf("invalid release version %q", release)
	}
	nums := [3]uint32{}
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(m[i+1], 10, 32)
		if err != nil {
			return fileVersion{}, fmt.Errorf("invalid release version %q", release)
		}
		nums[i] = uint32(n)
	}
	if nums[0] > 255 || nums[1] > 255 || nums[2] > 65535 {
		return fileVersion{}, fmt.Errorf("release version %q exceeds MSI field bounds", release)
	}
	fv := fileVersion{
		Major:   uint16(nums[0]),
		Minor:   uint16(nums[1]),
		Patch:   uint16(nums[2]),
		Product: release,
	}
	fv.Numeric = fmt.Sprintf("%d.%d.%d.%d", fv.Major, fv.Minor, fv.Patch, fv.Build)
	return fv, nil
}

// VersionInfo builds a VERSIONINFO resource. String order and padding match
// the Windows message compiler / windres layout.
func VersionInfo(info Info) ([]byte, error) {
	for _, s := range []string{info.InternalName, info.OriginalFilename, info.FileDescription, info.Release, info.Company, info.Product, info.Copyright} {
		if s == "" || containsNUL(s) {
			return nil, fmt.Errorf("version resource field is empty or contains NUL")
		}
	}
	ver, err := parseRelease(info.Release)
	if err != nil {
		return nil, err
	}
	var b []byte
	b = append(b, 0, 0, 52, 0, 0, 0)
	b = appendUTF16Z(b, "VS_VERSION_INFO")
	b = align4(b)
	b = appendFixed(b, ver)
	strStart := len(b)
	b = beginText(b, "StringFileInfo")
	tableStart := len(b)
	b = beginText(b, "040904B0")
	b = putString(b, "CompanyName", info.Company)
	b = putString(b, "FileDescription", info.FileDescription)
	b = putString(b, "FileVersion", ver.Numeric)
	b = putString(b, "InternalName", info.InternalName)
	b = putString(b, "LegalCopyright", info.Copyright)
	b = putString(b, "OriginalFilename", info.OriginalFilename)
	b = putString(b, "ProductName", info.Product)
	b = putString(b, "ProductVersion", ver.Product)
	finish(b, tableStart)
	finish(b, strStart)
	varStart := len(b)
	b = beginText(b, "VarFileInfo")
	b = putBinary(b, "Translation", []byte{0x09, 0x04, 0xB0, 0x04})
	finish(b, varStart)
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	return b, nil
}

func containsNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

func appendFixed(b []byte, ver fileVersion) []byte {
	var fixed [52]byte
	binary.LittleEndian.PutUint32(fixed[0:], 0xFEEF04BD)
	binary.LittleEndian.PutUint32(fixed[4:], 0x00010000)
	binary.LittleEndian.PutUint32(fixed[8:], uint32(ver.Major)<<16|uint32(ver.Minor))
	binary.LittleEndian.PutUint32(fixed[12:], uint32(ver.Patch)<<16|uint32(ver.Build))
	binary.LittleEndian.PutUint32(fixed[16:], uint32(ver.Major)<<16|uint32(ver.Minor))
	binary.LittleEndian.PutUint32(fixed[20:], uint32(ver.Patch)<<16|uint32(ver.Build))
	binary.LittleEndian.PutUint32(fixed[24:], 0x3F)
	binary.LittleEndian.PutUint32(fixed[32:], 0x00040004)
	binary.LittleEndian.PutUint32(fixed[36:], 0x1)
	return append(b, fixed[:]...)
}

func beginText(b []byte, key string) []byte {
	b = append(b, 0, 0, 0, 0, 1, 0)
	b = appendUTF16Z(b, key)
	return align4(b)
}

func finish(b []byte, start int) {
	binary.LittleEndian.PutUint16(b[start:], uint16(len(b)-start))
}

func putString(b []byte, key, value string) []byte {
	start := len(b)
	b = append(b, 0, 0, 0, 0, 1, 0)
	b = appendUTF16Z(b, key)
	b = align4(b)
	valAt := len(b)
	b = appendUTF16Z(b, value)
	binary.LittleEndian.PutUint16(b[start:], uint16(len(b)-start))
	binary.LittleEndian.PutUint16(b[start+2:], uint16((len(b)-valAt)/2))
	return align4(b)
}

func putBinary(b []byte, key string, value []byte) []byte {
	start := len(b)
	b = append(b, 0, 0, 0, 0, 0, 0)
	b = appendUTF16Z(b, key)
	b = align4(b)
	b = append(b, value...)
	binary.LittleEndian.PutUint16(b[start:], uint16(len(b)-start))
	binary.LittleEndian.PutUint16(b[start+2:], uint16(len(value)))
	return align4(b)
}

func appendUTF16Z(b []byte, s string) []byte {
	u := utf16.Encode([]rune(s))
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return append(b, 0, 0)
}

func align4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
