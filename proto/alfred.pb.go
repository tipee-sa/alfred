// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.28.1
// 	protoc        v4.24.4
// source: proto/alfred.proto

package proto

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type LoadImageResponse_Status int32

const (
	LoadImageResponse_UNSPECIFIED LoadImageResponse_Status = 0
	LoadImageResponse_CONTINUE    LoadImageResponse_Status = 1
	LoadImageResponse_OK          LoadImageResponse_Status = 2
)

// Enum value maps for LoadImageResponse_Status.
var (
	LoadImageResponse_Status_name = map[int32]string{
		0: "UNSPECIFIED",
		1: "CONTINUE",
		2: "OK",
	}
	LoadImageResponse_Status_value = map[string]int32{
		"UNSPECIFIED": 0,
		"CONTINUE":    1,
		"OK":          2,
	}
)

func (x LoadImageResponse_Status) Enum() *LoadImageResponse_Status {
	p := new(LoadImageResponse_Status)
	*p = x
	return p
}

func (x LoadImageResponse_Status) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (LoadImageResponse_Status) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_alfred_proto_enumTypes[0].Descriptor()
}

func (LoadImageResponse_Status) Type() protoreflect.EnumType {
	return &file_proto_alfred_proto_enumTypes[0]
}

func (x LoadImageResponse_Status) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use LoadImageResponse_Status.Descriptor instead.
func (LoadImageResponse_Status) EnumDescriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{2, 0}
}

type Job struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Name  string   `protobuf:"bytes,1,opt,name=name,proto3" json:"name,omitempty"`
	Image string   `protobuf:"bytes,2,opt,name=image,proto3" json:"image,omitempty"`
	Tasks []string `protobuf:"bytes,3,rep,name=tasks,proto3" json:"tasks,omitempty"`
}

func (x *Job) Reset() {
	*x = Job{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *Job) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Job) ProtoMessage() {}

func (x *Job) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Job.ProtoReflect.Descriptor instead.
func (*Job) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{0}
}

func (x *Job) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *Job) GetImage() string {
	if x != nil {
		return x.Image
	}
	return ""
}

func (x *Job) GetTasks() []string {
	if x != nil {
		return x.Tasks
	}
	return nil
}

type LoadImageMessage struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Message:
	//
	//	*LoadImageMessage_Init_
	//	*LoadImageMessage_Data_
	//	*LoadImageMessage_Done_
	Message isLoadImageMessage_Message `protobuf_oneof:"message"`
}

func (x *LoadImageMessage) Reset() {
	*x = LoadImageMessage{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *LoadImageMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*LoadImageMessage) ProtoMessage() {}

func (x *LoadImageMessage) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use LoadImageMessage.ProtoReflect.Descriptor instead.
func (*LoadImageMessage) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{1}
}

func (m *LoadImageMessage) GetMessage() isLoadImageMessage_Message {
	if m != nil {
		return m.Message
	}
	return nil
}

func (x *LoadImageMessage) GetInit() *LoadImageMessage_Init {
	if x, ok := x.GetMessage().(*LoadImageMessage_Init_); ok {
		return x.Init
	}
	return nil
}

func (x *LoadImageMessage) GetData() *LoadImageMessage_Data {
	if x, ok := x.GetMessage().(*LoadImageMessage_Data_); ok {
		return x.Data
	}
	return nil
}

func (x *LoadImageMessage) GetDone() *LoadImageMessage_Done {
	if x, ok := x.GetMessage().(*LoadImageMessage_Done_); ok {
		return x.Done
	}
	return nil
}

type isLoadImageMessage_Message interface {
	isLoadImageMessage_Message()
}

type LoadImageMessage_Init_ struct {
	Init *LoadImageMessage_Init `protobuf:"bytes,1,opt,name=init,proto3,oneof"`
}

type LoadImageMessage_Data_ struct {
	Data *LoadImageMessage_Data `protobuf:"bytes,2,opt,name=data,proto3,oneof"`
}

type LoadImageMessage_Done_ struct {
	Done *LoadImageMessage_Done `protobuf:"bytes,3,opt,name=done,proto3,oneof"`
}

func (*LoadImageMessage_Init_) isLoadImageMessage_Message() {}

func (*LoadImageMessage_Data_) isLoadImageMessage_Message() {}

func (*LoadImageMessage_Done_) isLoadImageMessage_Message() {}

type LoadImageResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Status    LoadImageResponse_Status `protobuf:"varint,1,opt,name=status,proto3,enum=proto.LoadImageResponse_Status" json:"status,omitempty"`
	ChunkSize *uint32                  `protobuf:"varint,2,opt,name=chunk_size,json=chunkSize,proto3,oneof" json:"chunk_size,omitempty"`
}

func (x *LoadImageResponse) Reset() {
	*x = LoadImageResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *LoadImageResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*LoadImageResponse) ProtoMessage() {}

func (x *LoadImageResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use LoadImageResponse.ProtoReflect.Descriptor instead.
func (*LoadImageResponse) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{2}
}

func (x *LoadImageResponse) GetStatus() LoadImageResponse_Status {
	if x != nil {
		return x.Status
	}
	return LoadImageResponse_UNSPECIFIED
}

func (x *LoadImageResponse) GetChunkSize() uint32 {
	if x != nil && x.ChunkSize != nil {
		return *x.ChunkSize
	}
	return 0
}

type ScheduleJobRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Job *Job `protobuf:"bytes,1,opt,name=job,proto3" json:"job,omitempty"`
}

func (x *ScheduleJobRequest) Reset() {
	*x = ScheduleJobRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *ScheduleJobRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ScheduleJobRequest) ProtoMessage() {}

func (x *ScheduleJobRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ScheduleJobRequest.ProtoReflect.Descriptor instead.
func (*ScheduleJobRequest) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{3}
}

func (x *ScheduleJobRequest) GetJob() *Job {
	if x != nil {
		return x.Job
	}
	return nil
}

type ScheduleJobResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *ScheduleJobResponse) Reset() {
	*x = ScheduleJobResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *ScheduleJobResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ScheduleJobResponse) ProtoMessage() {}

func (x *ScheduleJobResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ScheduleJobResponse.ProtoReflect.Descriptor instead.
func (*ScheduleJobResponse) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{4}
}

type PingRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *PingRequest) Reset() {
	*x = PingRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PingRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PingRequest) ProtoMessage() {}

func (x *PingRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PingRequest.ProtoReflect.Descriptor instead.
func (*PingRequest) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{5}
}

type PingResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Version string `protobuf:"bytes,1,opt,name=version,proto3" json:"version,omitempty"`
	Commit  string `protobuf:"bytes,2,opt,name=commit,proto3" json:"commit,omitempty"`
}

func (x *PingResponse) Reset() {
	*x = PingResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[6]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PingResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PingResponse) ProtoMessage() {}

func (x *PingResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[6]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PingResponse.ProtoReflect.Descriptor instead.
func (*PingResponse) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{6}
}

func (x *PingResponse) GetVersion() string {
	if x != nil {
		return x.Version
	}
	return ""
}

func (x *PingResponse) GetCommit() string {
	if x != nil {
		return x.Commit
	}
	return ""
}

type LoadImageMessage_Init struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	ImageId string `protobuf:"bytes,1,opt,name=image_id,json=imageId,proto3" json:"image_id,omitempty"`
}

func (x *LoadImageMessage_Init) Reset() {
	*x = LoadImageMessage_Init{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[7]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *LoadImageMessage_Init) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*LoadImageMessage_Init) ProtoMessage() {}

func (x *LoadImageMessage_Init) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[7]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use LoadImageMessage_Init.ProtoReflect.Descriptor instead.
func (*LoadImageMessage_Init) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{1, 0}
}

func (x *LoadImageMessage_Init) GetImageId() string {
	if x != nil {
		return x.ImageId
	}
	return ""
}

type LoadImageMessage_Data struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Chunk  []byte `protobuf:"bytes,1,opt,name=chunk,proto3" json:"chunk,omitempty"`
	Length uint32 `protobuf:"varint,2,opt,name=length,proto3" json:"length,omitempty"`
}

func (x *LoadImageMessage_Data) Reset() {
	*x = LoadImageMessage_Data{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[8]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *LoadImageMessage_Data) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*LoadImageMessage_Data) ProtoMessage() {}

func (x *LoadImageMessage_Data) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[8]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use LoadImageMessage_Data.ProtoReflect.Descriptor instead.
func (*LoadImageMessage_Data) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{1, 1}
}

func (x *LoadImageMessage_Data) GetChunk() []byte {
	if x != nil {
		return x.Chunk
	}
	return nil
}

func (x *LoadImageMessage_Data) GetLength() uint32 {
	if x != nil {
		return x.Length
	}
	return 0
}

type LoadImageMessage_Done struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *LoadImageMessage_Done) Reset() {
	*x = LoadImageMessage_Done{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proto_alfred_proto_msgTypes[9]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *LoadImageMessage_Done) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*LoadImageMessage_Done) ProtoMessage() {}

func (x *LoadImageMessage_Done) ProtoReflect() protoreflect.Message {
	mi := &file_proto_alfred_proto_msgTypes[9]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use LoadImageMessage_Done.ProtoReflect.Descriptor instead.
func (*LoadImageMessage_Done) Descriptor() ([]byte, []int) {
	return file_proto_alfred_proto_rawDescGZIP(), []int{1, 2}
}

var File_proto_alfred_proto protoreflect.FileDescriptor

var file_proto_alfred_proto_rawDesc = []byte{
	0x0a, 0x12, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x61, 0x6c, 0x66, 0x72, 0x65, 0x64, 0x2e, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x12, 0x05, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x45, 0x0a, 0x03, 0x4a,
	0x6f, 0x62, 0x12, 0x12, 0x0a, 0x04, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09,
	0x52, 0x04, 0x6e, 0x61, 0x6d, 0x65, 0x12, 0x14, 0x0a, 0x05, 0x69, 0x6d, 0x61, 0x67, 0x65, 0x18,
	0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x05, 0x69, 0x6d, 0x61, 0x67, 0x65, 0x12, 0x14, 0x0a, 0x05,
	0x74, 0x61, 0x73, 0x6b, 0x73, 0x18, 0x03, 0x20, 0x03, 0x28, 0x09, 0x52, 0x05, 0x74, 0x61, 0x73,
	0x6b, 0x73, 0x22, 0x9a, 0x02, 0x0a, 0x10, 0x4c, 0x6f, 0x61, 0x64, 0x49, 0x6d, 0x61, 0x67, 0x65,
	0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x12, 0x32, 0x0a, 0x04, 0x69, 0x6e, 0x69, 0x74, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x1c, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x4c, 0x6f,
	0x61, 0x64, 0x49, 0x6d, 0x61, 0x67, 0x65, 0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x2e, 0x49,
	0x6e, 0x69, 0x74, 0x48, 0x00, 0x52, 0x04, 0x69, 0x6e, 0x69, 0x74, 0x12, 0x32, 0x0a, 0x04, 0x64,
	0x61, 0x74, 0x61, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x1c, 0x2e, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x2e, 0x4c, 0x6f, 0x61, 0x64, 0x49, 0x6d, 0x61, 0x67, 0x65, 0x4d, 0x65, 0x73, 0x73, 0x61,
	0x67, 0x65, 0x2e, 0x44, 0x61, 0x74, 0x61, 0x48, 0x00, 0x52, 0x04, 0x64, 0x61, 0x74, 0x61, 0x12,
	0x32, 0x0a, 0x04, 0x64, 0x6f, 0x6e, 0x65, 0x18, 0x03, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x1c, 0x2e,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x4c, 0x6f, 0x61, 0x64, 0x49, 0x6d, 0x61, 0x67, 0x65, 0x4d,
	0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x2e, 0x44, 0x6f, 0x6e, 0x65, 0x48, 0x00, 0x52, 0x04, 0x64,
	0x6f, 0x6e, 0x65, 0x1a, 0x21, 0x0a, 0x04, 0x49, 0x6e, 0x69, 0x74, 0x12, 0x19, 0x0a, 0x08, 0x69,
	0x6d, 0x61, 0x67, 0x65, 0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x07, 0x69,
	0x6d, 0x61, 0x67, 0x65, 0x49, 0x64, 0x1a, 0x34, 0x0a, 0x04, 0x44, 0x61, 0x74, 0x61, 0x12, 0x14,
	0x0a, 0x05, 0x63, 0x68, 0x75, 0x6e, 0x6b, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x05, 0x63,
	0x68, 0x75, 0x6e, 0x6b, 0x12, 0x16, 0x0a, 0x06, 0x6c, 0x65, 0x6e, 0x67, 0x74, 0x68, 0x18, 0x02,
	0x20, 0x01, 0x28, 0x0d, 0x52, 0x06, 0x6c, 0x65, 0x6e, 0x67, 0x74, 0x68, 0x1a, 0x06, 0x0a, 0x04,
	0x44, 0x6f, 0x6e, 0x65, 0x42, 0x09, 0x0a, 0x07, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x22,
	0xb0, 0x01, 0x0a, 0x11, 0x4c, 0x6f, 0x61, 0x64, 0x49, 0x6d, 0x61, 0x67, 0x65, 0x52, 0x65, 0x73,
	0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x37, 0x0a, 0x06, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x1f, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x4c, 0x6f,
	0x61, 0x64, 0x49, 0x6d, 0x61, 0x67, 0x65, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x2e,
	0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x52, 0x06, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x12, 0x22,
	0x0a, 0x0a, 0x63, 0x68, 0x75, 0x6e, 0x6b, 0x5f, 0x73, 0x69, 0x7a, 0x65, 0x18, 0x02, 0x20, 0x01,
	0x28, 0x0d, 0x48, 0x00, 0x52, 0x09, 0x63, 0x68, 0x75, 0x6e, 0x6b, 0x53, 0x69, 0x7a, 0x65, 0x88,
	0x01, 0x01, 0x22, 0x2f, 0x0a, 0x06, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x12, 0x0f, 0x0a, 0x0b,
	0x55, 0x4e, 0x53, 0x50, 0x45, 0x43, 0x49, 0x46, 0x49, 0x45, 0x44, 0x10, 0x00, 0x12, 0x0c, 0x0a,
	0x08, 0x43, 0x4f, 0x4e, 0x54, 0x49, 0x4e, 0x55, 0x45, 0x10, 0x01, 0x12, 0x06, 0x0a, 0x02, 0x4f,
	0x4b, 0x10, 0x02, 0x42, 0x0d, 0x0a, 0x0b, 0x5f, 0x63, 0x68, 0x75, 0x6e, 0x6b, 0x5f, 0x73, 0x69,
	0x7a, 0x65, 0x22, 0x32, 0x0a, 0x12, 0x53, 0x63, 0x68, 0x65, 0x64, 0x75, 0x6c, 0x65, 0x4a, 0x6f,
	0x62, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x1c, 0x0a, 0x03, 0x6a, 0x6f, 0x62, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x0a, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x4a, 0x6f,
	0x62, 0x52, 0x03, 0x6a, 0x6f, 0x62, 0x22, 0x15, 0x0a, 0x13, 0x53, 0x63, 0x68, 0x65, 0x64, 0x75,
	0x6c, 0x65, 0x4a, 0x6f, 0x62, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x22, 0x0d, 0x0a,
	0x0b, 0x50, 0x69, 0x6e, 0x67, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x22, 0x40, 0x0a, 0x0c,
	0x50, 0x69, 0x6e, 0x67, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x18, 0x0a, 0x07,
	0x76, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x07, 0x76,
	0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e, 0x12, 0x16, 0x0a, 0x06, 0x63, 0x6f, 0x6d, 0x6d, 0x69, 0x74,
	0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x06, 0x63, 0x6f, 0x6d, 0x6d, 0x69, 0x74, 0x32, 0xc3,
	0x01, 0x0a, 0x06, 0x41, 0x6c, 0x66, 0x72, 0x65, 0x64, 0x12, 0x42, 0x0a, 0x09, 0x4c, 0x6f, 0x61,
	0x64, 0x49, 0x6d, 0x61, 0x67, 0x65, 0x12, 0x17, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x4c,
	0x6f, 0x61, 0x64, 0x49, 0x6d, 0x61, 0x67, 0x65, 0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x1a,
	0x18, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x4c, 0x6f, 0x61, 0x64, 0x49, 0x6d, 0x61, 0x67,
	0x65, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x28, 0x01, 0x30, 0x01, 0x12, 0x44, 0x0a,
	0x0b, 0x53, 0x63, 0x68, 0x65, 0x64, 0x75, 0x6c, 0x65, 0x4a, 0x6f, 0x62, 0x12, 0x19, 0x2e, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x53, 0x63, 0x68, 0x65, 0x64, 0x75, 0x6c, 0x65, 0x4a, 0x6f, 0x62,
	0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x1a, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e,
	0x53, 0x63, 0x68, 0x65, 0x64, 0x75, 0x6c, 0x65, 0x4a, 0x6f, 0x62, 0x52, 0x65, 0x73, 0x70, 0x6f,
	0x6e, 0x73, 0x65, 0x12, 0x2f, 0x0a, 0x04, 0x50, 0x69, 0x6e, 0x67, 0x12, 0x12, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x2e, 0x50, 0x69, 0x6e, 0x67, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a,
	0x13, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x50, 0x69, 0x6e, 0x67, 0x52, 0x65, 0x73, 0x70,
	0x6f, 0x6e, 0x73, 0x65, 0x42, 0x22, 0x5a, 0x20, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63,
	0x6f, 0x6d, 0x2f, 0x67, 0x61, 0x6d, 0x6d, 0x61, 0x64, 0x69, 0x61, 0x2f, 0x61, 0x6c, 0x66, 0x72,
	0x65, 0x64, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_proto_alfred_proto_rawDescOnce sync.Once
	file_proto_alfred_proto_rawDescData = file_proto_alfred_proto_rawDesc
)

func file_proto_alfred_proto_rawDescGZIP() []byte {
	file_proto_alfred_proto_rawDescOnce.Do(func() {
		file_proto_alfred_proto_rawDescData = protoimpl.X.CompressGZIP(file_proto_alfred_proto_rawDescData)
	})
	return file_proto_alfred_proto_rawDescData
}

var file_proto_alfred_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_proto_alfred_proto_msgTypes = make([]protoimpl.MessageInfo, 10)
var file_proto_alfred_proto_goTypes = []interface{}{
	(LoadImageResponse_Status)(0), // 0: proto.LoadImageResponse.Status
	(*Job)(nil),                   // 1: proto.Job
	(*LoadImageMessage)(nil),      // 2: proto.LoadImageMessage
	(*LoadImageResponse)(nil),     // 3: proto.LoadImageResponse
	(*ScheduleJobRequest)(nil),    // 4: proto.ScheduleJobRequest
	(*ScheduleJobResponse)(nil),   // 5: proto.ScheduleJobResponse
	(*PingRequest)(nil),           // 6: proto.PingRequest
	(*PingResponse)(nil),          // 7: proto.PingResponse
	(*LoadImageMessage_Init)(nil), // 8: proto.LoadImageMessage.Init
	(*LoadImageMessage_Data)(nil), // 9: proto.LoadImageMessage.Data
	(*LoadImageMessage_Done)(nil), // 10: proto.LoadImageMessage.Done
}
var file_proto_alfred_proto_depIdxs = []int32{
	8,  // 0: proto.LoadImageMessage.init:type_name -> proto.LoadImageMessage.Init
	9,  // 1: proto.LoadImageMessage.data:type_name -> proto.LoadImageMessage.Data
	10, // 2: proto.LoadImageMessage.done:type_name -> proto.LoadImageMessage.Done
	0,  // 3: proto.LoadImageResponse.status:type_name -> proto.LoadImageResponse.Status
	1,  // 4: proto.ScheduleJobRequest.job:type_name -> proto.Job
	2,  // 5: proto.Alfred.LoadImage:input_type -> proto.LoadImageMessage
	4,  // 6: proto.Alfred.ScheduleJob:input_type -> proto.ScheduleJobRequest
	6,  // 7: proto.Alfred.Ping:input_type -> proto.PingRequest
	3,  // 8: proto.Alfred.LoadImage:output_type -> proto.LoadImageResponse
	5,  // 9: proto.Alfred.ScheduleJob:output_type -> proto.ScheduleJobResponse
	7,  // 10: proto.Alfred.Ping:output_type -> proto.PingResponse
	8,  // [8:11] is the sub-list for method output_type
	5,  // [5:8] is the sub-list for method input_type
	5,  // [5:5] is the sub-list for extension type_name
	5,  // [5:5] is the sub-list for extension extendee
	0,  // [0:5] is the sub-list for field type_name
}

func init() { file_proto_alfred_proto_init() }
func file_proto_alfred_proto_init() {
	if File_proto_alfred_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_proto_alfred_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*Job); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*LoadImageMessage); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*LoadImageResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*ScheduleJobRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*ScheduleJobResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[5].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PingRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[6].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PingResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[7].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*LoadImageMessage_Init); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[8].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*LoadImageMessage_Data); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proto_alfred_proto_msgTypes[9].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*LoadImageMessage_Done); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	file_proto_alfred_proto_msgTypes[1].OneofWrappers = []interface{}{
		(*LoadImageMessage_Init_)(nil),
		(*LoadImageMessage_Data_)(nil),
		(*LoadImageMessage_Done_)(nil),
	}
	file_proto_alfred_proto_msgTypes[2].OneofWrappers = []interface{}{}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_proto_alfred_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   10,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_proto_alfred_proto_goTypes,
		DependencyIndexes: file_proto_alfred_proto_depIdxs,
		EnumInfos:         file_proto_alfred_proto_enumTypes,
		MessageInfos:      file_proto_alfred_proto_msgTypes,
	}.Build()
	File_proto_alfred_proto = out.File
	file_proto_alfred_proto_rawDesc = nil
	file_proto_alfred_proto_goTypes = nil
	file_proto_alfred_proto_depIdxs = nil
}