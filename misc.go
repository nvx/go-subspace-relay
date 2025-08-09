package subspacerelay

const (
	ContentTypeProto = "application/proto"
)

func NotZero[T comparable](vals ...T) T {
	var zero T
	for _, v := range vals {
		if v != zero {
			return v
		}
	}
	return zero
}

func Must[T any](val T, err error) T {
	if err != nil {
		panic(err)
	}
	return val
}
