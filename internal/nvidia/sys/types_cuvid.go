package sys

// cudaVideoCodec values.
const (
	CodecMPEG1 uint32 = 0
	CodecMPEG2 uint32 = 1
	CodecMPEG4 uint32 = 2
	CodecVC1   uint32 = 3
	CodecH264  uint32 = 4
	CodecJPEG  uint32 = 5
	CodecHEVC  uint32 = 8
	CodecVP8   uint32 = 9
	CodecVP9   uint32 = 10
	CodecAV1   uint32 = 11
)

// cudaVideoChromaFormat values.
const (
	ChromaMonochrome uint32 = 0
	Chroma420        uint32 = 1
	Chroma422        uint32 = 2
	Chroma444        uint32 = 3
)

// cudaVideoSurfaceFormat values.
const (
	SurfaceNV12      uint32 = 0
	SurfaceP016      uint32 = 1
	SurfaceYUV444    uint32 = 2
	SurfaceYUV444_16 uint32 = 3
)

// cudaVideoDeinterlaceMode values.
const (
	DeinterlaceWeave    uint32 = 0
	DeinterlaceBob      uint32 = 1
	DeinterlaceAdaptive uint32 = 2
)

// cudaVideoCreateFlags values.
const (
	CreateDefault     ULong = 0x00
	CreatePreferCUDA  ULong = 0x01
	CreatePreferDXVA  ULong = 0x02
	CreatePreferCUVID ULong = 0x04
)

// CUvideopacketflags values.
const (
	PktEndOfStream   ULong = 0x01
	PktTimestamp     ULong = 0x02
	PktDiscontinuity ULong = 0x04
	PktEndOfPicture  ULong = 0x08
	PktNotifyEOS     ULong = 0x10
)

// cuvidDecodeStatus values.
const (
	DecodeStatusInvalid        uint32 = 0
	DecodeStatusInProgress     uint32 = 1
	DecodeStatusSuccess        uint32 = 2
	DecodeStatusError          uint32 = 8
	DecodeStatusErrorConcealed uint32 = 9
)

// Rect is the int rectangle used by CUVIDEOFORMAT.display_area.
type Rect struct {
	Left, Top, Right, Bottom int32
}

// ShortRect is the short rectangle used by CUVIDDECODECREATEINFO.
type ShortRect struct {
	Left, Top, Right, Bottom int16
}

// VideoFormat mirrors CUVIDEOFORMAT, the sequence description the parser
// hands to the sequence callback.
type VideoFormat struct {
	Codec                   uint32
	FrameRateNumerator      uint32
	FrameRateDenominator    uint32
	ProgressiveSequence     uint8
	BitDepthLumaMinus8      uint8
	BitDepthChromaMinus8    uint8
	MinNumDecodeSurfaces    uint8
	CodedWidth              uint32
	CodedHeight             uint32
	DisplayArea             Rect
	ChromaFormat            uint32
	Bitrate                 uint32
	DisplayAspectRatioX     int32
	DisplayAspectRatioY     int32
	VideoSignal             uint8 // video_format:3, video_full_range_flag:1
	ColorPrimaries          uint8
	TransferCharacteristics uint8
	MatrixCoefficients      uint8
	SeqHdrDataLength        uint32
}

// VideoFullRange reports video_full_range_flag of the sequence.
func (f *VideoFormat) VideoFullRange() bool { return f.VideoSignal&0x08 != 0 }

// ParserParams mirrors CUVIDPARSERPARAMS.
type ParserParams struct {
	CodecType            uint32
	MaxNumDecodeSurfaces uint32
	ClockRate            uint32
	ErrorThreshold       uint32
	MaxDisplayDelay      uint32
	Flags                uint32 // bAnnexb:1 (AV1 only)
	Reserved1            [4]uint32
	UserData             uintptr
	SequenceCallback     uintptr // int (*)(void*, CUVIDEOFORMAT*)
	DecodePicture        uintptr // int (*)(void*, CUVIDPICPARAMS*)
	DisplayPicture       uintptr // int (*)(void*, CUVIDPARSERDISPINFO*)
	GetOperatingPoint    uintptr // int (*)(void*, CUVIDOPERATINGPOINTINFO*)
	Reserved2            [6]uintptr
	ExtVideoInfo         uintptr // *CUVIDEOFORMATEX, optional
}

// SourceDataPacket mirrors CUVIDSOURCEDATAPACKET. The flags and size are
// unsigned long, so the layout differs between Linux and Windows.
type SourceDataPacket struct {
	Flags       ULong
	PayloadSize ULong
	Payload     *byte
	Timestamp   int64
}

// ParserDispInfo mirrors CUVIDPARSERDISPINFO.
type ParserDispInfo struct {
	PictureIndex     int32
	ProgressiveFrame int32
	TopFieldFirst    int32
	RepeatFirstField int32
	Timestamp        int64
}

// DecodeCaps mirrors CUVIDDECODECAPS.
type DecodeCaps struct {
	CodecType            uint32
	ChromaFormat         uint32
	BitDepthMinus8       uint32
	Reserved1            [3]uint32
	IsSupported          uint8
	NumNVDECs            uint8
	OutputFormatMask     uint16
	MaxWidth             uint32
	MaxHeight            uint32
	MaxMBCount           uint32
	MinWidth             uint16
	MinHeight            uint16
	IsHistogramSupported uint8
	CounterBitDepth      uint8
	MaxHistogramBins     uint16
	Reserved3            [10]uint32
}

// DecodeCreateInfo mirrors CUVIDDECODECREATEINFO. Its tcu_ulong fields are
// unsigned long, so the layout differs between Linux and Windows.
type DecodeCreateInfo struct {
	Width             ULong
	Height            ULong
	NumDecodeSurfaces ULong
	CodecType         uint32
	ChromaFormat      uint32
	CreationFlags     ULong
	BitDepthMinus8    ULong
	IntraDecodeOnly   ULong
	MaxWidth          ULong
	MaxHeight         ULong
	Reserved1         ULong
	DisplayArea       ShortRect
	OutputFormat      uint32
	DeinterlaceMode   uint32
	TargetWidth       ULong
	TargetHeight      ULong
	NumOutputSurfaces ULong
	VidLock           uintptr
	TargetRect        ShortRect
	EnableHistogram   ULong
	Reserved2         [4]ULong
}

// PicParamsCodecSize is the size of the codec-specific union at the end of
// CUVIDPICPARAMS.
const PicParamsCodecSize = 4096

// DecodePicParams mirrors CUVIDPICPARAMS. The parser fills it completely
// (including the codec-specific union, kept here as raw bytes) and the
// decode callback passes it on to cuvidDecodePicture unchanged.
type DecodePicParams struct {
	PicWidthInMbs    int32
	FrameHeightInMbs int32
	CurrPicIdx       int32
	FieldPicFlag     int32
	BottomFieldFlag  int32
	SecondField      int32
	BitstreamDataLen uint32
	BitstreamData    uintptr
	NumSlices        uint32
	SliceDataOffsets uintptr
	RefPicFlag       int32
	IntraPicFlag     int32
	Reserved         [30]uint32
	CodecSpecific    [PicParamsCodecSize]byte
}

// ProcParams mirrors CUVIDPROCPARAMS.
type ProcParams struct {
	ProgressiveFrame int32
	SecondField      int32
	TopFieldFirst    int32
	UnpairedField    int32
	ReservedFlags    uint32
	ReservedZero     uint32
	RawInputDptr     uint64
	RawInputPitch    uint32
	RawInputFormat   uint32
	RawOutputDptr    uint64
	RawOutputPitch   uint32
	Reserved1        uint32
	OutputStream     uintptr
	Reserved         [46]uint32
	HistogramDptr    uintptr
	Reserved2        [1]uintptr
}

// GetDecodeStatus mirrors CUVIDGETDECODESTATUS.
type GetDecodeStatus struct {
	DecodeStatus uint32
	Reserved     [31]uint32
	PReserved    [8]uintptr
}
