package blocksync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 失败注入点（不真杀进程，只在对应阶段返回错误，模拟“写到一半被掐掉”）。
const (
	// StageBeforeWrite 在临时文件写入数据之前掐断。
	StageBeforeWrite = "before_write"
	// StageMidWrite 在数据写了一半之后、写完之前掐断。
	StageMidWrite = "mid_write"
	// StageBeforeRename 在临时文件已 fsync、原子改名之前掐断。
	StageBeforeRename = "before_rename"
)

// FailFunc 是失败注入钩子：入参为阶段名与块序号，返回 error 表示“此刻断电”。
type FailFunc func(stage string, index int) error

// ErrBadBlockSize 表示交给 Store 的数据不是一个完整固定块。
var ErrBadBlockSize = errors.New("blocksync: block must be exactly blockSize bytes")

// ErrOutOfOrder 表示追加的块序号不是当前末尾的下一块；后面的块不许抢到前面。
var ErrOutOfOrder = errors.New("blocksync: blocks must be appended in strict order")

// Store 把同步得到的块按原始顺序逐块落盘。
// 一块对应一个文件（blk-%010d），临时文件为 .part，原子改名提交。
type Store struct {
	dir       string
	blockSize int
	fail      FailFunc
	count     int
}

// OpenStore 打开（或创建）一个块存储目录，并执行崩溃恢复：
// 只有从序号 0 开始连续、文件大小完整的块才算已落完整；空洞之后的块、
// 改名前留下的 .part 都不能被当成有效块，会被清掉，后续块也不会排到前面。
func OpenStore(dir string, blockSize int, fail FailFunc) (*Store, error) {
	if blockSize <= 0 {
		return nil, errors.New("blocksync: blockSize must be positive")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	valid := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".part") {
			// 改名前的半成品，绝不能提交。
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		idx, ok := parseBlockName(name)
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() && info.Size() == int64(blockSize) {
			valid[name] = true
			_ = idx
		} else {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}

	// 只保留从 0 开始的连续前缀；空洞之后的块全部删除，绝不允许乱序可见。
	count := 0
	for {
		name := blockFileName(count)
		if !valid[name] {
			break
		}
		count++
	}
	for name := range valid {
		idx, _ := parseBlockName(name)
		if idx >= count {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}

	return &Store{dir: dir, blockSize: blockSize, fail: fail, count: count}, nil
}

// Count 返回当前已完整落盘的块数。
func (s *Store) Count() int { return s.count }

// Append 以严格顺序追加一个完整块。index 必须等于当前块数。
// 失败注入会在不同阶段制造“写到一半被掐掉”，返回注入的错误且本块不提交。
func (s *Store) Append(index int, block []byte) error {
	if len(block) != s.blockSize {
		return ErrBadBlockSize
	}
	if index != s.count {
		return fmt.Errorf("%w: want index %d, got %d", ErrOutOfOrder, s.count, index)
	}

	final := filepath.Join(s.dir, blockFileName(index))
	tmp := filepath.Join(s.dir, fmt.Sprintf(".blk-%010d.part", index))

	if s.fail != nil {
		if err := s.fail(StageBeforeWrite, index); err != nil {
			return err
		}
	}

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	if s.fail != nil {
		if ferr := s.fail(StageMidWrite, index); ferr != nil {
			// 只写一半，模拟断电：半块留在盘上，关闭后由恢复流程当垃圾清掉。
			half := len(block) / 2
			if half > 0 {
				_, _ = f.Write(block[:half])
				_ = f.Sync()
			}
			_ = f.Close()
			return ferr
		}
	}

	if _, err := f.Write(block); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	if s.fail != nil {
		if err := s.fail(StageBeforeRename, index); err != nil {
			return err
		}
	}

	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	if err := syncDir(s.dir); err != nil {
		return err
	}

	s.count++
	return nil
}

// Block 读出指定序号的完整块。
func (s *Store) Block(index int) ([]byte, error) {
	if index < 0 || index >= s.count {
		return nil, fmt.Errorf("blocksync: block %d out of range [0,%d)", index, s.count)
	}
	return os.ReadFile(filepath.Join(s.dir, blockFileName(index)))
}

// Blocks 按原始顺序返回全部已完整落盘的块（返回拷贝，可安全修改）。
func (s *Store) Blocks() ([][]byte, error) {
	out := make([][]byte, 0, s.count)
	for i := 0; i < s.count; i++ {
		b, err := s.Block(i)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// Reopen 模拟进程被掐掉后重新打开同一目录做恢复。
func (s *Store) Reopen() (*Store, error) {
	return OpenStore(s.dir, s.blockSize, s.fail)
}

func blockFileName(index int) string { return fmt.Sprintf("blk-%010d", index) }

func parseBlockName(name string) (int, bool) {
	if !strings.HasPrefix(name, "blk-") {
		return 0, false
	}
	idx, err := strconv.Atoi(strings.TrimPrefix(name, "blk-"))
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	if errors.Is(err, fs.ErrInvalid) {
		return nil
	}
	return err
}
