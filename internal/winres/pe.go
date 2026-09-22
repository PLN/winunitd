package winres

import (
	"encoding/binary"
	"fmt"
	"os"
)

const (
	pe32Plus            = 0x20b
	imageDirectoryEntry = 2
	sectionHeaderSize   = 40
	optCheckSum         = 64
	optSizeOfImage      = 56
	optNumberOfRva      = 108
	optDataDirectory    = 112
	scnInitializedData  = 0x00000040
	scnMemRead          = 0x40000000
)

// Stamp writes VERSIONINFO, and for the daemon a message table, into a
// Windows executable. The release string is the human product version. The
// numeric file version is its first three fields plus a zero build field.
func Stamp(path string, info Info) error {
	mode := os.FileMode(0o755)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := StampBytes(data, info)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, mode)
}

// StampBytes returns image with a new .rsrc section. image must be a PE32+
// executable with room for one more section header and no existing resource
// directory.
func StampBytes(image []byte, info Info) ([]byte, error) {
	version, err := VersionInfo(info)
	if err != nil {
		return nil, err
	}
	messages := []byte{}
	if info.IncludeMessages {
		messages = MessageTable()
	}
	return addResourceSection(image, func(rva uint32) []byte {
		if info.IncludeMessages {
			return ResourceSection(rva, version, messages)
		}
		return ResourceSection(rva, version, nil)
	})
}

func addResourceSection(image []byte, section func(rva uint32) []byte) ([]byte, error) {
	if len(image) < 0x40 {
		return nil, fmt.Errorf("pe image is too small")
	}
	lfanew := int(binary.LittleEndian.Uint32(image[0x3c:]))
	if lfanew <= 0 || lfanew+24 > len(image) || string(image[lfanew:lfanew+4]) != "PE\x00\x00" {
		return nil, fmt.Errorf("pe signature missing")
	}
	coff := lfanew + 4
	nsect := int(binary.LittleEndian.Uint16(image[coff+2:]))
	optSize := int(binary.LittleEndian.Uint16(image[coff+16:]))
	opt := coff + 20
	if opt+optSize > len(image) || optSize < optDataDirectory+8*3 {
		return nil, fmt.Errorf("optional header is truncated")
	}
	if binary.LittleEndian.Uint16(image[opt:]) != pe32Plus {
		return nil, fmt.Errorf("pe image is not PE32+")
	}
	if binary.LittleEndian.Uint32(image[opt+optNumberOfRva:]) < 3 {
		return nil, fmt.Errorf("pe data directories are truncated")
	}
	resOff := opt + optDataDirectory + imageDirectoryEntry*8
	if binary.LittleEndian.Uint32(image[resOff:]) != 0 || binary.LittleEndian.Uint32(image[resOff+4:]) != 0 {
		return nil, fmt.Errorf("pe image already has a resource directory")
	}
	sectionAlign := binary.LittleEndian.Uint32(image[opt+32:])
	fileAlign := binary.LittleEndian.Uint32(image[opt+36:])
	sizeOfHeaders := binary.LittleEndian.Uint32(image[opt+60:])
	if sectionAlign == 0 || fileAlign == 0 {
		return nil, fmt.Errorf("pe alignment is unset")
	}
	secTable := opt + optSize
	headersEnd := secTable + (nsect+1)*sectionHeaderSize
	if uint32(headersEnd) > sizeOfHeaders || headersEnd > len(image) {
		return nil, fmt.Errorf("pe headers have no room for a resource section")
	}
	var nextVA uint32
	rawEnd := uint32(len(image))
	for i := 0; i < nsect; i++ {
		hdr := image[secTable+i*sectionHeaderSize:]
		virtualSize := binary.LittleEndian.Uint32(hdr[8:])
		virtualAddress := binary.LittleEndian.Uint32(hdr[12:])
		rawSize := binary.LittleEndian.Uint32(hdr[16:])
		rawPtr := binary.LittleEndian.Uint32(hdr[20:])
		end := align32(virtualAddress+virtualSize, sectionAlign)
		if end > nextVA {
			nextVA = end
		}
		if rawPtr+rawSize > rawEnd {
			rawEnd = rawPtr + rawSize
		}
	}
	if nextVA == 0 {
		return nil, fmt.Errorf("pe image has no sections")
	}
	blob := section(nextVA)
	if len(blob) == 0 || len(blob) > 0xFFFF {
		return nil, fmt.Errorf("resource section size %d is invalid", len(blob))
	}
	rawPtr := align32(rawEnd, fileAlign)
	rawSize := align32(uint32(len(blob)), fileAlign)
	out := make([]byte, int(rawPtr)+int(rawSize))
	copy(out, image)
	for i := len(image); i < len(out); i++ {
		out[i] = 0
	}
	copy(out[rawPtr:], blob)
	writeSection(out[secTable+nsect*sectionHeaderSize:], uint32(len(blob)), nextVA, rawSize, rawPtr)
	binary.LittleEndian.PutUint16(out[coff+2:], uint16(nsect+1))
	binary.LittleEndian.PutUint32(out[opt+optSizeOfImage:], align32(nextVA+uint32(len(blob)), sectionAlign))
	binary.LittleEndian.PutUint32(out[resOff:], nextVA)
	binary.LittleEndian.PutUint32(out[resOff+4:], uint32(len(blob)))
	binary.LittleEndian.PutUint32(out[opt+optCheckSum:], 0)
	binary.LittleEndian.PutUint32(out[opt+optCheckSum:], peChecksum(out, opt+optCheckSum))
	return out, nil
}

func writeSection(b []byte, virtualSize, virtualAddress, rawSize, rawPtr uint32) {
	copy(b[:8], []byte(".rsrc\x00\x00\x00"))
	binary.LittleEndian.PutUint32(b[8:], virtualSize)
	binary.LittleEndian.PutUint32(b[12:], virtualAddress)
	binary.LittleEndian.PutUint32(b[16:], rawSize)
	binary.LittleEndian.PutUint32(b[20:], rawPtr)
	binary.LittleEndian.PutUint32(b[36:], scnInitializedData|scnMemRead)
}

func align32(n, a uint32) uint32 {
	if a == 0 {
		return n
	}
	return (n + a - 1) & ^(a - 1)
}

func peChecksum(data []byte, checksumOff int) uint32 {
	var sum uint32
	for i := 0; i+1 < len(data); i += 2 {
		if i == checksumOff || i == checksumOff+2 {
			continue
		}
		sum += uint32(binary.LittleEndian.Uint16(data[i:]))
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1])
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	sum = (sum & 0xFFFF) + (sum >> 16)
	sum += uint32(len(data))
	return sum
}
