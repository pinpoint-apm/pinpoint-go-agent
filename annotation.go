package pinpoint

import (
	"sync"

	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
)

// annotation stores recorded annotations as compact value structs instead of
// eagerly building the protobuf object graph. Append* runs on the application
// hot path, so it only copies the raw values into a slice (one amortized
// allocation) rather than allocating the 3-8 protobuf objects each annotation
// shape needs. The protobuf messages are materialized lazily in getList(),
// which the agent calls on its sender goroutine at serialization time.
type annotation struct {
	values         []annotationValue
	annotationLock sync.Mutex
}

type annotationValueType uint8

const (
	annotationTypeInt annotationValueType = iota
	annotationTypeLong
	annotationTypeString
	annotationTypeStringString
	annotationTypeIntStringString
	annotationTypeBytesStringString
	annotationTypeLongIntIntByteByteString
)

// annotationValue holds the raw fields for every annotation shape. It is stored
// by value in a slice; only the fields relevant to typ are populated.
type annotationValue struct {
	key   int32
	typ   annotationValueType
	i1    int32
	i2    int32
	b1    int32
	b2    int32
	l     int64
	s1    string
	s2    string
	bytes []byte
}

func (a *annotation) append(v annotationValue) {
	a.annotationLock.Lock()
	a.values = append(a.values, v)
	a.annotationLock.Unlock()
}

func (a *annotation) AppendInt(key int32, i int32) {
	a.append(annotationValue{key: key, typ: annotationTypeInt, i1: i})
}

func (a *annotation) AppendLong(key int32, l int64) {
	a.append(annotationValue{key: key, typ: annotationTypeLong, l: l})
}

func (a *annotation) AppendString(key int32, s string) {
	a.append(annotationValue{key: key, typ: annotationTypeString, s1: s})
}

func (a *annotation) AppendStringString(key int32, s1 string, s2 string) {
	a.append(annotationValue{key: key, typ: annotationTypeStringString, s1: s1, s2: s2})
}

func (a *annotation) AppendIntStringString(key int32, i int32, s1 string, s2 string) {
	a.append(annotationValue{key: key, typ: annotationTypeIntStringString, i1: i, s1: s1, s2: s2})
}

func (a *annotation) AppendBytesStringString(key int32, bs []byte, s1 string, s2 string) {
	a.append(annotationValue{key: key, typ: annotationTypeBytesStringString, bytes: bs, s1: s1, s2: s2})
}

func (a *annotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
	a.append(annotationValue{key: key, typ: annotationTypeLongIntIntByteByteString, l: l, i1: i1, i2: i2, b1: b1, b2: b2, s1: s})
}

// toProtoInto builds the protobuf annotation for a stored value on the
// builder's slabs. Called off the hot path (sender goroutine) during
// serialization; the result dies when the builder is released.
func (v *annotationValue) toProtoInto(b *spanMessageBuilder) *pb.PAnnotation {
	value := b.annotationValues.get()

	switch v.typ {
	case annotationTypeInt:
		oneof := b.intOneofs.get()
		oneof.IntValue = v.i1
		value.Field = oneof
	case annotationTypeLong:
		oneof := b.longOneofs.get()
		oneof.LongValue = v.l
		value.Field = oneof
	case annotationTypeString:
		oneof := b.stringOneofs.get()
		oneof.StringValue = v.s1
		value.Field = oneof
	case annotationTypeStringString:
		inner := b.stringStrings.get()
		inner.StringValue1 = b.stringValue(v.s1)
		inner.StringValue2 = b.stringValue(v.s2)
		oneof := b.stringStringOneofs.get()
		oneof.StringStringValue = inner
		value.Field = oneof
	case annotationTypeIntStringString:
		inner := b.intStringStrings.get()
		inner.IntValue = v.i1
		inner.StringValue1 = b.stringValue(v.s1)
		inner.StringValue2 = b.stringValue(v.s2)
		oneof := b.intStringStringOneofs.get()
		oneof.IntStringStringValue = inner
		value.Field = oneof
	case annotationTypeBytesStringString:
		inner := b.bytesStringStrings.get()
		inner.BytesValue = v.bytes
		inner.StringValue1 = b.stringValue(v.s1)
		inner.StringValue2 = b.stringValue(v.s2)
		oneof := b.bytesStringStringOneofs.get()
		oneof.BytesStringStringValue = inner
		value.Field = oneof
	case annotationTypeLongIntIntByteByteString:
		inner := b.longIntIntByteByteStrings.get()
		inner.LongValue = v.l
		inner.IntValue1 = v.i1
		inner.IntValue2 = v.i2
		inner.ByteValue1 = v.b1
		inner.ByteValue2 = v.b2
		inner.StringValue = b.stringValue(v.s1)
		oneof := b.longIntIntByteByteStringOneofs.get()
		oneof.LongIntIntByteByteStringValue = inner
		value.Field = oneof
	}

	annotation := b.annotations.get()
	annotation.Key = v.key
	annotation.Value = value
	return annotation
}

// getListInto materializes the annotation list on the builder's slabs.
func (a *annotation) getListInto(b *spanMessageBuilder) []*pb.PAnnotation {
	a.annotationLock.Lock()
	defer a.annotationLock.Unlock()

	if len(a.values) == 0 {
		return nil
	}

	list := b.annotationLists.take(len(a.values))
	for i := range a.values {
		list[i] = a.values[i].toProtoInto(b)
	}
	return list
}

// getList materializes the list on a throwaway builder, so the result is
// plainly GC-owned. Non-transport callers (JsonString) use this.
func (a *annotation) getList() []*pb.PAnnotation {
	return a.getListInto(&spanMessageBuilder{})
}
