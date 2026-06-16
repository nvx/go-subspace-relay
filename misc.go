package subspacerelay

const (
	ContentTypeProto = "application/proto"
)

func Must[T any](val T, err error) T {
	if err != nil {
		panic(err)
	}
	return val
}
