package config

import (
	"fmt"
	"os"
	"strconv"
)

func strconvParseOctal(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("mode %q is not a valid octal value (e.g. 0644)", s)
	}
	return os.FileMode(v), nil
}
