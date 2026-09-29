package runtime

import (
	"fmt"
	"strings"
)

func ComposeSupported(v string) bool {
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	var major, minor, patch int
	if _, e := fmt.Sscanf(v, "%d.%d.%d", &major, &minor, &patch); e != nil {
		return false
	}
	return major == 2 && minor >= 30 || major > 2
}
