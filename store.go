package blocksync

import (
	"encoding/binary"
	"fmt"
	"io"
)

// SyncFile 是落盘所需的最小接口，测试可注入失败点。
type SyncFile interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Truncate(size int64) error
}

const countSize = 8

// Store 崩溃安全地按块落盘：先写块记录并刷盘，再推进已提交计数。
// 重新打开时只认计数内的完整块，写到一半的块被丢弃，
// 后面的块不可能抢到前面。
type Store struct {
	f         SyncFile
	blockSize int
	count     int
}

// OpenStore 打开或创建存储，并恢复到上次一致的状态。
func OpenStore(f SyncFile, blockSize int) (*Store, error) {
	s := &Store{f: f, blockSize: blockSize}
	var hdr [countSize]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil {
		// 空文件：初始化计数为 0。
		if err := s.writeCount(); err != nil {
			return nil, err
		}
		return s, nil
	}
	s.count = int(binary.BigEndian.Uint64(hdr[:]))
	// 校验计数内的块记录都完整，截掉写了一半的尾巴。
	off := int64(countSize)
	valid := 0
	var lenBuf [4]byte
	for i := 0; i < s.count; i++ {
		if _, err := f.ReadAt(lenBuf[:], off); err != nil {
			break
		}
		l := int64(binary.BigEndian.Uint32(lenBuf[:]))
		if _, err := f.ReadAt(make([]byte, l), off+4); err != nil {
			break
		}
		off += 4 + l
		valid++
	}
	if valid != s.count {
		s.count = valid
		if err := s.writeCount(); err != nil {
			return nil, err
		}
	}
	if err := f.Truncate(off); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) writeCount() error {
	var hdr [countSize]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(s.count))
	if _, err := s.f.WriteAt(hdr[:], 0); err != nil {
		return err
	}
	return s.f.Sync()
}

// Append 落完整一块并提交。任何一步失败都视为没写这块。
func (s *Store) Append(block []byte) error {
	if len(block) > s.blockSize {
		return fmt.Errorf("blocksync: 块长 %d 超过块上限 %d", len(block), s.blockSize)
	}
	off, err := s.tail()
	if err != nil {
		return err
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(block)))
	if _, err := s.f.WriteAt(lenBuf[:], off); err != nil {
		return err
	}
	if _, err := s.f.WriteAt(block, off+4); err != nil {
		return err
	}
	if err := s.f.Sync(); err != nil {
		return err
	}
	s.count++
	return s.writeCount()
}

func (s *Store) tail() (int64, error) {
	off := int64(countSize)
	var lenBuf [4]byte
	for i := 0; i < s.count; i++ {
		if _, err := s.f.ReadAt(lenBuf[:], off); err != nil {
			return 0, err
		}
		off += 4 + int64(binary.BigEndian.Uint32(lenBuf[:]))
	}
	return off, nil
}

// Blocks 按原顺序吐出已落完整的块，一块不多。
func (s *Store) Blocks() ([][]byte, error) {
	out := make([][]byte, 0, s.count)
	off := int64(countSize)
	var lenBuf [4]byte
	for i := 0; i < s.count; i++ {
		if _, err := s.f.ReadAt(lenBuf[:], off); err != nil {
			return nil, err
		}
		l := int64(binary.BigEndian.Uint32(lenBuf[:]))
		blk := make([]byte, l)
		if _, err := s.f.ReadAt(blk, off+4); err != nil {
			return nil, err
		}
		out = append(out, blk)
		off += 4 + l
	}
	return out, nil
}

// Bytes 把已提交的块按原顺序拼成一段字节流。
func (s *Store) Bytes() ([]byte, error) {
	blocks, err := s.Blocks()
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, b := range blocks {
		out = append(out, b...)
	}
	return out, nil
}
