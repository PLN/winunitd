package winres

import "encoding/binary"

const (
	rtMessageTable = 11
	rtVersion      = 16
	langEnglishUS  = 0x0409
)

type resNode struct {
	id         uint32
	data       []byte
	kids       []*resNode
	dirOff     int
	dataEntOff int
	rawOff     int
}

// ResourceSection builds a PE resource directory. Data-entry RVAs are
// sectionRVA plus the offset of each blob. An empty message table is omitted.
func ResourceSection(sectionRVA uint32, version, messages []byte) []byte {
	root := &resNode{}
	if len(messages) > 0 {
		root.kids = append(root.kids, leafType(rtMessageTable, messages))
	}
	root.kids = append(root.kids, leafType(rtVersion, version))
	size := layout(root)
	buf := make([]byte, size)
	writeRes(buf, root, sectionRVA)
	return buf
}

func leafType(typeID uint32, data []byte) *resNode {
	return &resNode{id: typeID, kids: []*resNode{{
		id: 1,
		kids: []*resNode{{
			id:   langEnglishUS,
			data: append([]byte(nil), data...),
		}},
	}}}
}

func layout(root *resNode) int {
	off := 0
	var leaves []*resNode
	var walk func(*resNode)
	walk = func(n *resNode) {
		if len(n.kids) == 0 {
			leaves = append(leaves, n)
			return
		}
		n.dirOff = off
		off += 16 + 8*len(n.kids)
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(root)
	for _, leaf := range leaves {
		leaf.dataEntOff = off
		off += 16
	}
	for _, leaf := range leaves {
		leaf.rawOff = off
		off = alignUp(off+len(leaf.data), 4)
	}
	return off
}

func writeRes(buf []byte, n *resNode, sectionRVA uint32) {
	if len(n.kids) == 0 {
		putDataEntry(buf[n.dataEntOff:], sectionRVA+uint32(n.rawOff), len(n.data))
		copy(buf[n.rawOff:], n.data)
		return
	}
	putDir(buf[n.dirOff:], 0, len(n.kids))
	for i, k := range n.kids {
		target, directory := k.dirOff, true
		if len(k.kids) == 0 {
			target, directory = k.dataEntOff, false
		}
		putDirID(buf[n.dirOff+16+i*8:], k.id, target, directory)
		writeRes(buf, k, sectionRVA)
	}
}

func alignUp(n, a int) int {
	if a <= 0 {
		return n
	}
	return (n + a - 1) & ^(a - 1)
}

func putDir(b []byte, named, ids int) {
	binary.LittleEndian.PutUint16(b[12:], uint16(named))
	binary.LittleEndian.PutUint16(b[14:], uint16(ids))
}

func putDirID(b []byte, id uint32, offset int, directory bool) {
	binary.LittleEndian.PutUint32(b[0:], id)
	off := uint32(offset)
	if directory {
		off |= 0x80000000
	}
	binary.LittleEndian.PutUint32(b[4:], off)
}

func putDataEntry(b []byte, rva uint32, size int) {
	binary.LittleEndian.PutUint32(b[0:], rva)
	binary.LittleEndian.PutUint32(b[4:], uint32(size))
}
