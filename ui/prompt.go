// Package ui 提供基于 bufio 的简单交互式菜单，纯标准库实现。
package ui

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var reader = bufio.NewReader(os.Stdin)

// PrintReader 暴露内部 reader 供需要读原始行的调用方使用
func PrintReader() *bufio.Reader {
	return reader
}

// ReadLine 读取一行输入（去除首尾空白）
func ReadLine() string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// Prompt 读取一行输入；如果用户回车则返回 defaultVal
func Prompt(label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	line := ReadLine()
	if line == "" {
		return defaultVal
	}
	return line
}

// PromptInt 读取一个整数
func PromptInt(label string, defaultVal int) int {
	if defaultVal != 0 {
		fmt.Printf("%s [%d]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	line := ReadLine()
	if line == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(line)
	if err != nil {
		fmt.Printf("  无效数字，使用默认值 %d\n", defaultVal)
		return defaultVal
	}
	return v
}

// PromptFloat 读取一个浮点数
func PromptFloat(label string, defaultVal float64) float64 {
	fmt.Printf("%s [%g]: ", label, defaultVal)
	line := ReadLine()
	if line == "" {
		return defaultVal
	}
	v, err := strconv.ParseFloat(line, 64)
	if err != nil {
		fmt.Printf("  无效浮点，使用默认值 %g\n", defaultVal)
		return defaultVal
	}
	return v
}

// SelectOption 是单选项
type SelectOption struct {
	Label string
	Desc  string
}

// Select 让用户从选项列表里选一项，返回选中的 index（0-based）
// 标题用于提示；allowNew 为 true 时末尾会追加一个「+ 新建」选项
func Select(title string, opts []SelectOption, allowNew bool) (int, bool) {
	for {
		fmt.Printf("\n%s\n", title)
		for i, o := range opts {
			if o.Desc != "" {
				fmt.Printf("  %2d) %s  — %s\n", i+1, o.Label, o.Desc)
			} else {
				fmt.Printf("  %2d) %s\n", i+1, o.Label)
			}
		}
		extra := 0
		if allowNew {
			fmt.Printf("  %2d) + 新建\n", len(opts)+1)
			extra = 1
		}
		fmt.Print("选择> ")
		line := ReadLine()
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(opts)+extra {
			fmt.Println("  无效输入，请重试")
			continue
		}
		if allowNew && n == len(opts)+1 {
			return -1, true
		}
		return n - 1, false
	}
}

// MultiSelect 让用户选择多个选项。输入格式支持 "1,3,5"、"1-4" 或混合。
func MultiSelect(title string, opts []SelectOption) []int {
	for {
		fmt.Printf("\n%s\n", title)
		for i, o := range opts {
			if o.Desc != "" {
				fmt.Printf("  %2d) %s  — %s\n", i+1, o.Label, o.Desc)
			} else {
				fmt.Printf("  %2d) %s\n", i+1, o.Label)
			}
		}
		fmt.Print("选择（可多选，如 1,3,5 或 1-4）> ")
		line := ReadLine()
		idxs, err := parseMultiSelect(line, len(opts))
		if err != nil {
			fmt.Printf("  %v，请重试\n", err)
			continue
		}
		return idxs
	}
}

func parseMultiSelect(line string, max int) ([]int, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("不能为空")
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.Split(part, "-")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("无效范围 %q", part)
			}
			start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
			if err != nil {
				return nil, fmt.Errorf("无效数字 %q", bounds[0])
			}
			end, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				return nil, fmt.Errorf("无效数字 %q", bounds[1])
			}
			if start > end {
				return nil, fmt.Errorf("无效范围 %q", part)
			}
			for n := start; n <= end; n++ {
				if err := addSelectIndex(&out, seen, n, max); err != nil {
					return nil, err
				}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("无效数字 %q", part)
		}
		if err := addSelectIndex(&out, seen, n, max); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("不能为空")
	}
	return out, nil
}

func addSelectIndex(out *[]int, seen map[int]bool, n, max int) error {
	if n < 1 || n > max {
		return fmt.Errorf("选项 %d 超出范围", n)
	}
	idx := n - 1
	if seen[idx] {
		return nil
	}
	seen[idx] = true
	*out = append(*out, idx)
	return nil
}

// Confirm 让用户确认 yes/no，默认回车等价于 defaultYes
func Confirm(label string, defaultYes bool) bool {
	suffix := "y/N"
	if defaultYes {
		suffix = "Y/n"
	}
	fmt.Printf("%s [%s]: ", label, suffix)
	line := strings.ToLower(ReadLine())
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}
