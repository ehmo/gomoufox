package policy

import "fmt"

type InputKind string

const (
	InputGeneral          InputKind = "general"
	InputScript           InputKind = "script"
	InputTypedText        InputKind = "typed_text"
	InputHeaders          InputKind = "headers"
	InputFetchBody        InputKind = "fetch_body"
	InputSessionLoadState InputKind = "session_load_state"
)

func ClampResponseCap(n int) (int, error) {
	if n <= 0 {
		return DefaultMaxResponseBytes, nil
	}
	if n > HardMaxResponseBytes {
		return 0, fmt.Errorf("max response bytes %d exceeds hard cap %d", n, HardMaxResponseBytes)
	}
	return n, nil
}

func ClampInputCap(n int) (int, error) {
	if n <= 0 {
		return DefaultMaxInputBytes, nil
	}
	if n > HardMaxInputBytes {
		return 0, fmt.Errorf("max input bytes %d exceeds hard cap %d", n, HardMaxInputBytes)
	}
	return n, nil
}

func ClampInputCapFor(kind InputKind, configured int) (int, error) {
	general, err := ClampInputCap(configured)
	if err != nil {
		return 0, err
	}
	kindCap, err := inputKindCap(kind)
	if err != nil {
		return 0, err
	}
	if general < kindCap {
		return general, nil
	}
	return kindCap, nil
}

func ScreenshotCap(fullPage bool, requested int) (int, error) {
	hard := DefaultScreenshotBytes
	if fullPage {
		hard = FullPageScreenshotBytes
	}
	if requested <= 0 {
		return hard, nil
	}
	if requested > hard {
		return 0, fmt.Errorf("screenshot max bytes %d exceeds hard cap %d", requested, hard)
	}
	return requested, nil
}

func Truncate(data []byte, cap int) ([]byte, bool) {
	if cap < 0 || len(data) <= cap {
		return data, false
	}
	return data[:cap], true
}

func inputKindCap(kind InputKind) (int, error) {
	switch kind {
	case InputGeneral:
		return DefaultMaxInputBytes, nil
	case InputScript:
		return ScriptInputBytes, nil
	case InputTypedText:
		return TypedTextInputBytes, nil
	case InputHeaders:
		return HeaderInputBytes, nil
	case InputFetchBody:
		return FetchBodyInputBytes, nil
	case InputSessionLoadState:
		return InlineSessionLoadBytes, nil
	default:
		return 0, fmt.Errorf("unknown input kind: %s", kind)
	}
}
