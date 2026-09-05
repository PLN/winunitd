package journal

import "io"

const journalBufferSize = 4096

type bufferedRecord struct {
	remaining    int
	messageBytes int
}

// recordBuffer accepts complete records and keeps the exact unwritten suffix
// after a partial write. It holds at most one oversized record or a small batch.
// Unlike bufio.Writer, a failed Flush can be retried without discarding bytes.
// The owning unitFile serializes all access.
type recordBuffer struct {
	writer  io.Writer
	data    []byte
	records []bufferedRecord
}

func (b *recordBuffer) append(raw []byte, messageBytes int) error {
	if len(b.data) > 0 && len(b.data)+len(raw) > journalBufferSize {
		if err := b.Flush(); err != nil {
			return err
		}
	}
	b.data = append(b.data, raw...)
	b.records = append(b.records, bufferedRecord{remaining: len(raw), messageBytes: messageBytes})
	return nil
}

func (b *recordBuffer) Flush() error {
	if len(b.data) == 0 {
		return nil
	}
	n, err := b.writer.Write(b.data)
	if n < 0 || n > len(b.data) {
		return io.ErrShortWrite
	}
	if n < len(b.data) && err == nil {
		err = io.ErrShortWrite
	}
	left := n
	for left > 0 && len(b.records) > 0 {
		if left < b.records[0].remaining {
			b.records[0].remaining -= left
			break
		}
		left -= b.records[0].remaining
		b.records = b.records[1:]
	}
	copy(b.data, b.data[n:])
	b.data = b.data[:len(b.data)-n]
	if len(b.data) == 0 && cap(b.data) > journalBufferSize {
		b.data = nil
	}
	return err
}

func (b *recordBuffer) pendingLoss() (records, messageBytes uint64) {
	for _, record := range b.records {
		records++
		messageBytes += uint64(record.messageBytes)
	}
	return
}
