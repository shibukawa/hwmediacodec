/*
 * fake_drv_video.c - a VA-API driver for tests.
 *
 * It decodes and encodes nothing. It exists so that the vaapi backend can
 * be run end to end through libva on a machine without a GPU, and so that
 * what the backend hands to a driver is checked through the C structure
 * definitions of the libva headers:
 *
 *  - every parameter buffer must have the size of its C structure;
 *  - a picture named as a reference must be in the surface the backend says
 *    it is in (the driver remembers which picture order count it "decoded"
 *    or "reconstructed" into every surface), which catches surfaces that
 *    are reused while still referenced;
 *  - slice parameters must agree with the slice data they describe;
 *  - encode pictures must carry the buffers a real driver needs.
 *
 * Decoding fills the target surface with a pattern derived from the
 * picture order count: Y(x,y) = poc + x + 2*y, Cb(x,y) = poc + x,
 * Cr(x,y) = poc + 3*y (mod 256, chroma in chroma sample coordinates).
 * AV1 has no picture order count: its pictures are numbered in decode
 * order instead, and a picture shown with film grain is painted with its
 * number plus 128 so that it can be told from the reference it leaves
 * behind. The log line of an AV1 picture carries the picture parameters and
 * the tile parameters in hexadecimal, with surface identifiers replaced by
 * picture numbers, so that two clients decoding the same stream (the
 * backend and ffmpeg's VA-API hwaccel) can be compared field by field.
 * Encoding returns the packed headers it was given followed by a fake slice
 * payload, and logs a checksum of the input surface.
 *
 * Environment:
 *   FAKE_VA_LOG         file that receives one line per picture and per error
 *   FAKE_VA_PACKED      VA_ENC_PACKED_HEADER_* mask the encoder reports (1)
 *   FAKE_VA_HEVC_ATTRS  0: do not report HEVC features and block sizes (1)
 *   FAKE_VA_DERIVE      0: vaDeriveImage fails, forcing vaGetImage/vaPutImage (1)
 *
 * Build: gcc -shared -fPIC -o fake_drv_video.so fake_drv_video.c
 * Use:   LIBVA_DRIVERS_PATH=<dir> LIBVA_DRIVER_NAME=fake
 */
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <va/va.h>
#include <va/va_backend.h>
#include <va/va_enc_hevc.h>

#define MAX_CONFIGS 16
#define MAX_CONTEXTS 16
#define MAX_SURFACES 256
#define MAX_BUFFERS 4096
#define MAX_IMAGES 64
#define MAX_PIC_BUFFERS 256

#define CONFIG_BASE 0x01000000u
#define CONTEXT_BASE 0x02000000u
#define SURFACE_BASE 0x04000000u
#define BUFFER_BASE 0x08000000u
#define IMAGE_BASE 0x10000000u

#define NO_POC 0x7fffffff

/* The backend marks frames inferred for a frame_num gap with this bit; libva
 * does not define it and drivers ignore it. */
#ifndef VA_PICTURE_H264_NON_EXISTING
#define VA_PICTURE_H264_NON_EXISTING 0x00000020
#endif

struct config {
    int used;
    VAProfile profile;
    VAEntrypoint entrypoint;
    unsigned rc;
};

struct surface {
    int used;
    int width, height; /* requested size */
    int pitch, rows;   /* allocated layout */
    uint8_t *data;     /* Y plane, then CbCr at pitch * rows */
    int poc;           /* picture decoded or reconstructed into it */
    int grainy;        /* AV1: the picture has film grain, so it is no reference */
    int uploaded;      /* written through an image since the last encode */
};

struct buffer {
    int used;
    VABufferType type;
    unsigned size, num;
    uint8_t *data;
    int surface; /* image buffers derived from a surface: its index, else -1 */
    VACodedBufferSegment segment;
};

struct image {
    int used;
    VAImage va;
    int derived;
};

struct context {
    int used;
    int config;
    int width, height;
    VASurfaceID targets[MAX_SURFACES];
    int num_targets;
    VASurfaceID target; /* of the picture in progress */
    VABufferID bufs[MAX_PIC_BUFFERS];
    int num_bufs;
    /* encoder state */
    int av1_pictures;  /* AV1 pictures decoded so far */
    int have_seq;
    unsigned ctb_log2; /* HEVC */
    unsigned mbs;      /* H.264 */
    int pictures;
};

static struct config configs[MAX_CONFIGS];
static struct context contexts[MAX_CONTEXTS];
static struct surface surfaces[MAX_SURFACES];
static struct buffer buffers[MAX_BUFFERS];
static struct image images[MAX_IMAGES];

static void logf_(const char *fmt, ...)
{
    const char *path = getenv("FAKE_VA_LOG");
    FILE *f;
    va_list ap;

    if (!path)
        return;
    f = fopen(path, "a");
    if (!f)
        return;
    va_start(ap, fmt);
    vfprintf(f, fmt, ap);
    va_end(ap);
    fputc('\n', f);
    fclose(f);
}

static int env_int(const char *name, int def)
{
    const char *v = getenv(name);
    return v && *v ? atoi(v) : def;
}

/* fail logs a check failure; the caller returns its result. */
static VAStatus fail(const char *fmt, ...)
{
    char msg[512];
    va_list ap;

    va_start(ap, fmt);
    vsnprintf(msg, sizeof msg, fmt, ap);
    va_end(ap);
    logf_("error %s", msg);
    fprintf(stderr, "fake VA driver: %s\n", msg);
    return VA_STATUS_ERROR_INVALID_PARAMETER;
}

static struct config *get_config(VAConfigID id)
{
    unsigned i = id - CONFIG_BASE;
    return i < MAX_CONFIGS && configs[i].used ? &configs[i] : NULL;
}

static struct context *get_context(VAContextID id)
{
    unsigned i = id - CONTEXT_BASE;
    return i < MAX_CONTEXTS && contexts[i].used ? &contexts[i] : NULL;
}

static struct surface *get_surface(VASurfaceID id)
{
    unsigned i = id - SURFACE_BASE;
    return i < MAX_SURFACES && surfaces[i].used ? &surfaces[i] : NULL;
}

static struct buffer *get_buffer(VABufferID id)
{
    unsigned i = id - BUFFER_BASE;
    return i < MAX_BUFFERS && buffers[i].used ? &buffers[i] : NULL;
}

static struct image *get_image(VAImageID id)
{
    unsigned i = id - IMAGE_BASE;
    return i < MAX_IMAGES && images[i].used ? &images[i] : NULL;
}

static int is_hevc(VAProfile p) { return p == VAProfileHEVCMain; }
static int is_h264(VAProfile p)
{
    return p == VAProfileH264ConstrainedBaseline || p == VAProfileH264Main || p == VAProfileH264High;
}
static int is_av1(VAProfile p) { return p == VAProfileAV1Profile0; }
static int is_known(VAProfile p) { return is_h264(p) || is_hevc(p) || is_av1(p); }
static int is_encode(VAEntrypoint e) { return e == VAEntrypointEncSlice; }
/* AV1 is decode only, as on GPUs that have an AV1 decoder and no encoder. */
static int has_entrypoint(VAProfile p, VAEntrypoint e)
{
    return e == VAEntrypointVLD || (is_encode(e) && !is_av1(p));
}

/* ---- configuration ---- */

static const VAProfile profiles[] = {
    VAProfileH264ConstrainedBaseline, VAProfileH264Main, VAProfileH264High, VAProfileHEVCMain,
    VAProfileAV1Profile0,
};

static VAStatus fake_QueryConfigProfiles(VADriverContextP ctx, VAProfile *list, int *num)
{
    memcpy(list, profiles, sizeof profiles);
    *num = sizeof profiles / sizeof profiles[0];
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_QueryConfigEntrypoints(VADriverContextP ctx, VAProfile profile, VAEntrypoint *list, int *num)
{
    if (!is_known(profile))
        return VA_STATUS_ERROR_UNSUPPORTED_PROFILE;
    list[0] = VAEntrypointVLD;
    list[1] = VAEntrypointEncSlice;
    *num = is_av1(profile) ? 1 : 2;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_GetConfigAttributes(VADriverContextP ctx, VAProfile profile, VAEntrypoint entrypoint,
                                         VAConfigAttrib *attribs, int num)
{
    int i;

    if (!is_known(profile))
        return VA_STATUS_ERROR_UNSUPPORTED_PROFILE;
    if (!has_entrypoint(profile, entrypoint))
        return VA_STATUS_ERROR_UNSUPPORTED_ENTRYPOINT;
    for (i = 0; i < num; i++) {
        unsigned v = VA_ATTRIB_NOT_SUPPORTED;
        switch (attribs[i].type) {
        case VAConfigAttribRTFormat:
            v = VA_RT_FORMAT_YUV420;
            break;
        case VAConfigAttribRateControl:
            if (is_encode(entrypoint))
                v = VA_RC_CQP | VA_RC_CBR | VA_RC_VBR;
            break;
        case VAConfigAttribEncPackedHeaders:
            if (is_encode(entrypoint))
                v = env_int("FAKE_VA_PACKED", VA_ENC_PACKED_HEADER_SEQUENCE);
            break;
        case VAConfigAttribEncMaxRefFrames:
            if (is_encode(entrypoint))
                v = 1;
            break;
        case VAConfigAttribMaxPictureWidth:
        case VAConfigAttribMaxPictureHeight:
            if (is_encode(entrypoint))
                v = 4096;
            break;
        case VAConfigAttribEncHEVCFeatures:
            if (is_encode(entrypoint) && is_hevc(profile) && env_int("FAKE_VA_HEVC_ATTRS", 1)) {
                VAConfigAttribValEncHEVCFeatures f;
                f.value = 0;
                f.bits.amp = VA_FEATURE_SUPPORTED;
                f.bits.sao = VA_FEATURE_SUPPORTED;
                f.bits.strong_intra_smoothing = VA_FEATURE_SUPPORTED;
                f.bits.constrained_intra_pred = VA_FEATURE_SUPPORTED;
                f.bits.cu_qp_delta = VA_FEATURE_SUPPORTED;
                v = f.value;
            }
            break;
        case VAConfigAttribEncHEVCBlockSizes:
            if (is_encode(entrypoint) && is_hevc(profile) && env_int("FAKE_VA_HEVC_ATTRS", 1)) {
                VAConfigAttribValEncHEVCBlockSizes b;
                b.value = 0;
                b.bits.log2_max_coding_tree_block_size_minus3 = 3;
                b.bits.log2_min_coding_tree_block_size_minus3 = 3;
                b.bits.log2_min_luma_coding_block_size_minus3 = 0;
                b.bits.log2_max_luma_transform_block_size_minus2 = 3;
                b.bits.log2_min_luma_transform_block_size_minus2 = 0;
                v = b.value;
            }
            break;
        default:
            break;
        }
        attribs[i].value = v;
    }
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_CreateConfig(VADriverContextP ctx, VAProfile profile, VAEntrypoint entrypoint,
                                  VAConfigAttrib *attribs, int num, VAConfigID *id)
{
    int i, j;

    if (!is_known(profile))
        return VA_STATUS_ERROR_UNSUPPORTED_PROFILE;
    if (!has_entrypoint(profile, entrypoint))
        return VA_STATUS_ERROR_UNSUPPORTED_ENTRYPOINT;
    for (i = 0; i < MAX_CONFIGS; i++) {
        if (configs[i].used)
            continue;
        memset(&configs[i], 0, sizeof configs[i]);
        configs[i].used = 1;
        configs[i].profile = profile;
        configs[i].entrypoint = entrypoint;
        configs[i].rc = VA_RC_CQP;
        for (j = 0; j < num; j++) {
            if (attribs[j].type == VAConfigAttribRTFormat && !(attribs[j].value & VA_RT_FORMAT_YUV420)) {
                configs[i].used = 0;
                return VA_STATUS_ERROR_UNSUPPORTED_RT_FORMAT;
            }
            if (attribs[j].type == VAConfigAttribRateControl)
                configs[i].rc = attribs[j].value;
            if (attribs[j].type == VAConfigAttribEncPackedHeaders &&
                (attribs[j].value & ~(unsigned)env_int("FAKE_VA_PACKED", VA_ENC_PACKED_HEADER_SEQUENCE))) {
                configs[i].used = 0;
                fail("config asks for packed headers %#x the driver did not offer", attribs[j].value);
                return VA_STATUS_ERROR_ATTR_NOT_SUPPORTED;
            }
        }
        *id = CONFIG_BASE + i;
        return VA_STATUS_SUCCESS;
    }
    return VA_STATUS_ERROR_MAX_NUM_EXCEEDED;
}

static VAStatus fake_DestroyConfig(VADriverContextP ctx, VAConfigID id)
{
    struct config *c = get_config(id);
    if (!c)
        return VA_STATUS_ERROR_INVALID_CONFIG;
    c->used = 0;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_QueryConfigAttributes(VADriverContextP ctx, VAConfigID id, VAProfile *profile,
                                           VAEntrypoint *entrypoint, VAConfigAttrib *attribs, int *num)
{
    struct config *c = get_config(id);
    if (!c)
        return VA_STATUS_ERROR_INVALID_CONFIG;
    *profile = c->profile;
    *entrypoint = c->entrypoint;
    *num = 0;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_QuerySurfaceAttributes(VADriverContextP ctx, VAConfigID id, VASurfaceAttrib *attribs,
                                            unsigned int *num)
{
    static const struct {
        VASurfaceAttribType type;
        int value;
    } list[] = {
        {VASurfaceAttribPixelFormat, VA_FOURCC_NV12},
        {VASurfaceAttribMinWidth, 16},
        {VASurfaceAttribMaxWidth, 4096},
        {VASurfaceAttribMinHeight, 16},
        {VASurfaceAttribMaxHeight, 2304},
    };
    unsigned n = sizeof list / sizeof list[0], i;

    if (!get_config(id))
        return VA_STATUS_ERROR_INVALID_CONFIG;
    if (!attribs) {
        *num = n;
        return VA_STATUS_SUCCESS;
    }
    if (*num < n) {
        *num = n;
        return VA_STATUS_ERROR_MAX_NUM_EXCEEDED;
    }
    for (i = 0; i < n; i++) {
        memset(&attribs[i], 0, sizeof attribs[i]);
        attribs[i].type = list[i].type;
        attribs[i].flags = VA_SURFACE_ATTRIB_GETTABLE;
        attribs[i].value.type = VAGenericValueTypeInteger;
        attribs[i].value.value.i = list[i].value;
    }
    *num = n;
    return VA_STATUS_SUCCESS;
}

/* ---- surfaces ---- */

static VAStatus fake_CreateSurfaces2(VADriverContextP ctx, unsigned int format, unsigned int width,
                                     unsigned int height, VASurfaceID *ids, unsigned int num,
                                     VASurfaceAttrib *attribs, unsigned int num_attribs)
{
    unsigned n, i, j;

    if (format != VA_RT_FORMAT_YUV420)
        return VA_STATUS_ERROR_UNSUPPORTED_RT_FORMAT;
    if (width < 16 || height < 16 || width > 4096 || height > 2304)
        return VA_STATUS_ERROR_RESOLUTION_NOT_SUPPORTED;
    for (j = 0; j < num_attribs; j++) {
        if (attribs[j].type == VASurfaceAttribPixelFormat &&
            (attribs[j].value.type != VAGenericValueTypeInteger || attribs[j].value.value.i != VA_FOURCC_NV12))
            return fail("surface pixel format attribute is not NV12 (type %d value %#x)",
                        attribs[j].value.type, attribs[j].value.value.i);
    }
    for (n = 0; n < num; n++) {
        for (i = 0; i < MAX_SURFACES && surfaces[i].used; i++)
            ;
        if (i == MAX_SURFACES)
            return VA_STATUS_ERROR_MAX_NUM_EXCEEDED;
        memset(&surfaces[i], 0, sizeof surfaces[i]);
        surfaces[i].used = 1;
        surfaces[i].width = width;
        surfaces[i].height = height;
        /* A pitch wider than the picture and rows beyond it, as real
         * drivers have. */
        surfaces[i].pitch = ((width + 63) & ~63) + 32;
        surfaces[i].rows = ((height + 15) & ~15) + 8;
        surfaces[i].data = malloc((size_t)surfaces[i].pitch * surfaces[i].rows * 3 / 2);
        if (!surfaces[i].data)
            return VA_STATUS_ERROR_ALLOCATION_FAILED;
        memset(surfaces[i].data, 0x55, (size_t)surfaces[i].pitch * surfaces[i].rows * 3 / 2);
        surfaces[i].poc = NO_POC;
        ids[n] = SURFACE_BASE + i;
    }
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_CreateSurfaces(VADriverContextP ctx, int width, int height, int format, int num,
                                    VASurfaceID *ids)
{
    return fake_CreateSurfaces2(ctx, format, width, height, ids, num, NULL, 0);
}

static VAStatus fake_DestroySurfaces(VADriverContextP ctx, VASurfaceID *ids, int num)
{
    int i;
    for (i = 0; i < num; i++) {
        struct surface *s = get_surface(ids[i]);
        if (!s)
            return fail("vaDestroySurfaces: surface %#x does not exist", ids[i]);
        free(s->data);
        s->used = 0;
    }
    return VA_STATUS_SUCCESS;
}

static uint8_t *chroma(struct surface *s) { return s->data + (size_t)s->pitch * s->rows; }

static void paint(struct surface *s, int poc)
{
    int x, y;
    uint8_t *c = chroma(s);

    for (y = 0; y < s->height; y++)
        for (x = 0; x < s->width; x++)
            s->data[y * s->pitch + x] = (uint8_t)(poc + x + 2 * y);
    for (y = 0; y < (s->height + 1) / 2; y++)
        for (x = 0; x < (s->width + 1) / 2; x++) {
            c[y * s->pitch + 2 * x] = (uint8_t)(poc + x);
            c[y * s->pitch + 2 * x + 1] = (uint8_t)(poc + 3 * y);
        }
    s->poc = poc;
}

/* ---- contexts ---- */

static VAStatus fake_CreateContext(VADriverContextP ctx, VAConfigID config, int width, int height, int flag,
                                   VASurfaceID *targets, int num_targets, VAContextID *id)
{
    int i, j;

    if (!get_config(config))
        return VA_STATUS_ERROR_INVALID_CONFIG;
    if (num_targets > MAX_SURFACES)
        return VA_STATUS_ERROR_MAX_NUM_EXCEEDED;
    for (j = 0; j < num_targets; j++) {
        struct surface *s = get_surface(targets[j]);
        if (!s)
            return fail("vaCreateContext: render target %#x does not exist", targets[j]);
        if (s->width != width || s->height != height)
            return fail("vaCreateContext: render target is %dx%d, the context %dx%d", s->width, s->height, width, height);
    }
    for (i = 0; i < MAX_CONTEXTS; i++) {
        if (contexts[i].used)
            continue;
        memset(&contexts[i], 0, sizeof contexts[i]);
        contexts[i].used = 1;
        contexts[i].config = config - CONFIG_BASE;
        contexts[i].width = width;
        contexts[i].height = height;
        memcpy(contexts[i].targets, targets, num_targets * sizeof *targets);
        contexts[i].num_targets = num_targets;
        contexts[i].target = VA_INVALID_SURFACE;
        *id = CONTEXT_BASE + i;
        return VA_STATUS_SUCCESS;
    }
    return VA_STATUS_ERROR_MAX_NUM_EXCEEDED;
}

static VAStatus fake_DestroyContext(VADriverContextP ctx, VAContextID id)
{
    struct context *c = get_context(id);
    if (!c)
        return VA_STATUS_ERROR_INVALID_CONTEXT;
    c->used = 0;
    return VA_STATUS_SUCCESS;
}

static int is_target(struct context *c, VASurfaceID id)
{
    int i;
    for (i = 0; i < c->num_targets; i++)
        if (c->targets[i] == id)
            return 1;
    return 0;
}

/* ---- buffers ---- */

static VAStatus new_buffer(VABufferType type, unsigned size, unsigned num, void *data, int surface, VABufferID *id)
{
    int i;

    for (i = 0; i < MAX_BUFFERS && buffers[i].used; i++)
        ;
    if (i == MAX_BUFFERS)
        return fail("more than %d buffers alive: buffers are leaking", MAX_BUFFERS);
    memset(&buffers[i], 0, sizeof buffers[i]);
    buffers[i].used = 1;
    buffers[i].type = type;
    buffers[i].size = size;
    buffers[i].num = num;
    buffers[i].surface = surface;
    if (surface < 0) {
        buffers[i].data = calloc(1, (size_t)size * num + 16);
        if (!buffers[i].data)
            return VA_STATUS_ERROR_ALLOCATION_FAILED;
        if (data)
            memcpy(buffers[i].data, data, (size_t)size * num);
    }
    *id = BUFFER_BASE + i;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_CreateBuffer(VADriverContextP ctx, VAContextID context, VABufferType type, unsigned int size,
                                  unsigned int num, void *data, VABufferID *id)
{
    if (!get_context(context))
        return fail("vaCreateBuffer: context %#x does not exist", context);
    if (size == 0 || num == 0)
        return fail("vaCreateBuffer: empty buffer of type %d", type);
    return new_buffer(type, size, num, data, -1, id);
}

static VAStatus fake_BufferSetNumElements(VADriverContextP ctx, VABufferID id, unsigned int num)
{
    return VA_STATUS_ERROR_UNIMPLEMENTED;
}

static VAStatus fake_MapBuffer(VADriverContextP ctx, VABufferID id, void **pbuf)
{
    struct buffer *b = get_buffer(id);
    if (!b)
        return fail("vaMapBuffer: buffer %#x does not exist", id);
    if (b->type == VAEncCodedBufferType)
        *pbuf = &b->segment;
    else if (b->surface >= 0)
        *pbuf = surfaces[b->surface].data;
    else
        *pbuf = b->data;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_UnmapBuffer(VADriverContextP ctx, VABufferID id)
{
    struct buffer *b = get_buffer(id);
    if (!b)
        return fail("vaUnmapBuffer: buffer %#x does not exist", id);
    if (b->surface >= 0)
        surfaces[b->surface].uploaded = 1;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_DestroyBuffer(VADriverContextP ctx, VABufferID id)
{
    struct buffer *b = get_buffer(id);
    if (!b)
        return fail("vaDestroyBuffer: buffer %#x does not exist (destroyed twice?)", id);
    free(b->data);
    b->used = 0;
    return VA_STATUS_SUCCESS;
}

/* ---- pictures ---- */

static VAStatus fake_BeginPicture(VADriverContextP ctx, VAContextID id, VASurfaceID target)
{
    struct context *c = get_context(id);
    if (!c)
        return VA_STATUS_ERROR_INVALID_CONTEXT;
    if (!get_surface(target))
        return fail("vaBeginPicture: surface %#x does not exist", target);
    if (c->target != VA_INVALID_SURFACE)
        return fail("vaBeginPicture while a picture is in progress");
    c->target = target;
    c->num_bufs = 0;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_RenderPicture(VADriverContextP ctx, VAContextID id, VABufferID *bufs, int num)
{
    struct context *c = get_context(id);
    int i;

    if (!c)
        return VA_STATUS_ERROR_INVALID_CONTEXT;
    if (c->target == VA_INVALID_SURFACE)
        return fail("vaRenderPicture without vaBeginPicture");
    for (i = 0; i < num; i++) {
        if (!get_buffer(bufs[i]))
            return fail("vaRenderPicture: buffer %#x does not exist", bufs[i]);
        if (c->num_bufs == MAX_PIC_BUFFERS)
            return fail("more than %d buffers in one picture", MAX_PIC_BUFFERS);
        c->bufs[c->num_bufs++] = bufs[i];
    }
    return VA_STATUS_SUCCESS;
}

/* find returns the n-th buffer of a type in the current picture. */
static struct buffer *find(struct context *c, VABufferType type, int n)
{
    int i;
    for (i = 0; i < c->num_bufs; i++) {
        struct buffer *b = get_buffer(c->bufs[i]);
        if (b->type == type && n-- == 0)
            return b;
    }
    return NULL;
}

static int count(struct context *c, VABufferType type)
{
    int i, n = 0;
    for (i = 0; i < c->num_bufs; i++)
        if (get_buffer(c->bufs[i])->type == type)
            n++;
    return n;
}

/* check_reference verifies that a surface holds the picture claimed. */
static VAStatus check_reference(const char *what, int index, VASurfaceID id, int poc)
{
    struct surface *s = get_surface(id);
    if (!s)
        return fail("%s %d: surface %#x does not exist", what, index, id);
    if (s->poc == NO_POC)
        return fail("%s %d: surface %#x was never decoded into (POC %d claimed)", what, index, id, poc);
    if (s->poc != poc)
        return fail("%s %d: surface %#x holds POC %d, the buffer claims POC %d", what, index, id, s->poc, poc);
    return VA_STATUS_SUCCESS;
}

/* slice_pairs checks that slice parameter and slice data buffers alternate
 * and returns the number of slices, or -1. */
static int slice_pairs(struct context *c, unsigned param_size)
{
    int i, n = 0, want_data = 0;

    for (i = 0; i < c->num_bufs; i++) {
        struct buffer *b = get_buffer(c->bufs[i]);
        if (b->type == VASliceParameterBufferType) {
            if (want_data) {
                fail("two slice parameter buffers without slice data between them");
                return -1;
            }
            if (b->size != param_size || b->num != 1) {
                fail("slice parameter buffer has %u x %u bytes, the structure has %u", b->size, b->num, param_size);
                return -1;
            }
            want_data = 1;
        } else if (b->type == VASliceDataBufferType) {
            if (!want_data) {
                fail("slice data without slice parameters");
                return -1;
            }
            want_data = 0;
            n++;
        }
    }
    if (want_data) {
        fail("slice parameters without slice data");
        return -1;
    }
    if (n == 0)
        fail("picture without slices");
    return n ? n : -1;
}

static VAStatus decode_hevc(struct context *c)
{
    struct buffer *pb = find(c, VAPictureParameterBufferType, 0);
    VAPictureParameterBufferHEVC *pp;
    int i, j, slices, before = 0, after = 0, lt = 0, refs = 0, seen_invalid = 0;
    struct surface *target = get_surface(c->target);
    VAStatus st;

    if (!pb || count(c, VAPictureParameterBufferType) != 1)
        return fail("hevc: need exactly one picture parameter buffer");
    if (pb->size != sizeof *pp)
        return fail("hevc: picture parameter buffer has %u bytes, VAPictureParameterBufferHEVC has %zu", pb->size, sizeof *pp);
    pp = (VAPictureParameterBufferHEVC *)pb->data;
    if (pp->CurrPic.picture_id != c->target)
        return fail("hevc: CurrPic names surface %#x, the render target is %#x", pp->CurrPic.picture_id, c->target);
    if (pp->CurrPic.flags != 0)
        return fail("hevc: CurrPic has flags %#x", pp->CurrPic.flags);
    if (pp->pic_width_in_luma_samples != c->width || pp->pic_height_in_luma_samples != c->height)
        return fail("hevc: picture is %ux%u, the context %dx%d", pp->pic_width_in_luma_samples,
                    pp->pic_height_in_luma_samples, c->width, c->height);
    if (pp->pic_fields.bits.chroma_format_idc != 1 || pp->bit_depth_luma_minus8 || pp->bit_depth_chroma_minus8)
        return fail("hevc: not 8-bit 4:2:0");
    for (i = 0; i < 15; i++) {
        VAPictureHEVC *r = &pp->ReferenceFrames[i];
        unsigned sets = r->flags & (VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE | VA_PICTURE_HEVC_RPS_ST_CURR_AFTER | VA_PICTURE_HEVC_RPS_LT_CURR);
        if (r->flags & VA_PICTURE_HEVC_INVALID) {
            if (r->picture_id != VA_INVALID_SURFACE)
                return fail("hevc: invalid reference %d has surface %#x", i, r->picture_id);
            seen_invalid = 1;
            continue;
        }
        if (seen_invalid)
            return fail("hevc: reference %d follows an empty entry", i);
        if (r->picture_id == c->target)
            return fail("hevc: reference %d is the render target", i);
        if ((st = check_reference("hevc reference", i, r->picture_id, r->pic_order_cnt)) != VA_STATUS_SUCCESS)
            return st;
        if (sets & (sets - 1))
            return fail("hevc: reference %d is in more than one reference picture set (flags %#x)", i, r->flags);
        if ((sets & VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE) && r->pic_order_cnt >= pp->CurrPic.pic_order_cnt)
            return fail("hevc: reference %d (POC %d) is ST_CURR_BEFORE of POC %d", i, r->pic_order_cnt, pp->CurrPic.pic_order_cnt);
        if ((sets & VA_PICTURE_HEVC_RPS_ST_CURR_AFTER) && r->pic_order_cnt <= pp->CurrPic.pic_order_cnt)
            return fail("hevc: reference %d (POC %d) is ST_CURR_AFTER of POC %d", i, r->pic_order_cnt, pp->CurrPic.pic_order_cnt);
        before += !!(sets & VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE);
        after += !!(sets & VA_PICTURE_HEVC_RPS_ST_CURR_AFTER);
        lt += !!(sets & VA_PICTURE_HEVC_RPS_LT_CURR);
        refs++;
    }
    if (refs > pp->sps_max_dec_pic_buffering_minus1)
        return fail("hevc: %d reference pictures with sps_max_dec_pic_buffering_minus1 = %u", refs, pp->sps_max_dec_pic_buffering_minus1);
    if (!!find(c, VAIQMatrixBufferType, 0) != !!pp->pic_fields.bits.scaling_list_enabled_flag)
        return fail("hevc: IQ matrix buffer presence does not match scaling_list_enabled_flag");
    if (find(c, VAIQMatrixBufferType, 0) && find(c, VAIQMatrixBufferType, 0)->size != sizeof(VAIQMatrixBufferHEVC))
        return fail("hevc: IQ matrix buffer has the wrong size");

    slices = slice_pairs(c, sizeof(VASliceParameterBufferHEVC));
    if (slices < 0)
        return VA_STATUS_ERROR_INVALID_PARAMETER;
    for (i = 0; i < slices; i++) {
        VASliceParameterBufferHEVC *sp = (VASliceParameterBufferHEVC *)find(c, VASliceParameterBufferType, i)->data;
        struct buffer *data = find(c, VASliceDataBufferType, i);
        unsigned type = (data->data[0] >> 1) & 0x3f, slice_type = sp->LongSliceFlags.fields.slice_type;
        int irap = type >= 16 && type <= 23, idr = type == 19 || type == 20;
        int first = data->data[2] >> 7;
        unsigned start = sp->slice_data_byte_offset + sp->slice_data_num_emu_prevn_bytes;

        if (sp->slice_data_size != data->size || sp->slice_data_offset != 0 || sp->slice_data_flag != VA_SLICE_DATA_FLAG_ALL)
            return fail("hevc slice %d: size %u offset %u flag %u for %u bytes of data", i, sp->slice_data_size,
                        sp->slice_data_offset, sp->slice_data_flag, data->size);
        if (data->data[0] & 0x80 || type > 21 || (type > 9 && type < 16))
            return fail("hevc slice %d: data does not start with a slice NAL unit header (%02x %02x)", i, data->data[0], data->data[1]);
        if (start < 3 || start >= data->size)
            return fail("hevc slice %d: slice data at byte %u of %u", i, start, data->size);
        if (first != (i == 0))
            return fail("hevc slice %d: first_slice_segment_in_pic_flag is %d", i, first);
        if ((i == 0) != (sp->slice_segment_address == 0))
            return fail("hevc slice %d: slice_segment_address %u", i, sp->slice_segment_address);
        if (pp->slice_parsing_fields.bits.IdrPicFlag != idr || pp->slice_parsing_fields.bits.RapPicFlag != irap)
            return fail("hevc slice %d: NAL type %u with IdrPicFlag %u RapPicFlag %u", i, type,
                        pp->slice_parsing_fields.bits.IdrPicFlag, pp->slice_parsing_fields.bits.RapPicFlag);
        if (sp->LongSliceFlags.fields.LastSliceOfPic != (i == slices - 1))
            return fail("hevc slice %d of %d: LastSliceOfPic is %u", i, slices, sp->LongSliceFlags.fields.LastSliceOfPic);
        if (slice_type > 2 || (irap && slice_type != 2))
            return fail("hevc slice %d: slice_type %u in NAL type %u", i, slice_type, type);
        if (idr && refs)
            return fail("hevc: IDR picture with %d reference pictures", refs);
        for (j = 0; j < 2; j++) {
            int k, n = 0, want = 0;
            if (j == 0 && slice_type != 2)
                want = sp->num_ref_idx_l0_active_minus1 + 1;
            if (j == 1 && slice_type == 0)
                want = sp->num_ref_idx_l1_active_minus1 + 1;
            for (k = 0; k < 15; k++) {
                uint8_t idx = sp->RefPicList[j][k];
                VAPictureHEVC *r;
                if (idx == 0xff)
                    continue;
                if (k != n)
                    return fail("hevc slice %d: RefPicList%d[%d] follows an unused entry", i, j, k);
                n++;
                if (idx >= 15 || (pp->ReferenceFrames[idx].flags & VA_PICTURE_HEVC_INVALID))
                    return fail("hevc slice %d: RefPicList%d[%d] = %u is not a reference frame", i, j, k, idx);
                r = &pp->ReferenceFrames[idx];
                if (!(r->flags & (VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE | VA_PICTURE_HEVC_RPS_ST_CURR_AFTER | VA_PICTURE_HEVC_RPS_LT_CURR)))
                    return fail("hevc slice %d: RefPicList%d[%d] is outside the current reference picture set", i, j, k);
            }
            if (n != want)
                return fail("hevc slice %d (type %u): RefPicList%d has %d entries, num_ref_idx says %d", i, slice_type, j, n, want);
        }
        if (slice_type != 2 && before + after + lt == 0)
            return fail("hevc slice %d: inter slice without current reference pictures", i);
    }
    paint(target, pp->CurrPic.pic_order_cnt);
    logf_("dec codec=hevc poc=%d surface=%u slices=%d refs=%d before=%d after=%d lt=%d st_rps_bits=%u", pp->CurrPic.pic_order_cnt,
          c->target - SURFACE_BASE, slices, refs, before, after, lt, pp->st_rps_bits);
    return VA_STATUS_SUCCESS;
}

static VAStatus decode_h264(struct context *c)
{
    struct buffer *pb = find(c, VAPictureParameterBufferType, 0), *iq = find(c, VAIQMatrixBufferType, 0);
    VAPictureParameterBufferH264 *pp;
    int i, j, slices, refs = 0;
    VAStatus st;

    if (!pb || count(c, VAPictureParameterBufferType) != 1)
        return fail("h264: need exactly one picture parameter buffer");
    if (pb->size != sizeof *pp)
        return fail("h264: picture parameter buffer has %u bytes, VAPictureParameterBufferH264 has %zu", pb->size, sizeof *pp);
    if (!iq || iq->size != sizeof(VAIQMatrixBufferH264))
        return fail("h264: IQ matrix buffer missing or of the wrong size");
    pp = (VAPictureParameterBufferH264 *)pb->data;
    if (pp->CurrPic.picture_id != c->target)
        return fail("h264: CurrPic names surface %#x, the render target is %#x", pp->CurrPic.picture_id, c->target);
    if ((pp->picture_width_in_mbs_minus1 + 1) * 16 != c->width || (pp->picture_height_in_mbs_minus1 + 1) * 16 != c->height)
        return fail("h264: picture is %ux%u macroblocks, the context %dx%d", pp->picture_width_in_mbs_minus1 + 1,
                    pp->picture_height_in_mbs_minus1 + 1, c->width, c->height);
    for (i = 0; i < 16; i++) {
        VAPictureH264 *r = &pp->ReferenceFrames[i];
        if (r->flags & VA_PICTURE_H264_INVALID)
            continue;
        refs++;
        if (!(r->flags & (VA_PICTURE_H264_SHORT_TERM_REFERENCE | VA_PICTURE_H264_LONG_TERM_REFERENCE)))
            return fail("h264: reference %d is neither short-term nor long-term (flags %#x)", i, r->flags);
        if (r->flags & VA_PICTURE_H264_NON_EXISTING)
            continue;
        if (r->picture_id == c->target)
            return fail("h264: reference %d is the render target", i);
        if ((st = check_reference("h264 reference", i, r->picture_id, r->TopFieldOrderCnt)) != VA_STATUS_SUCCESS)
            return st;
    }
    if (refs > pp->num_ref_frames && pp->num_ref_frames)
        return fail("h264: %d reference frames with num_ref_frames = %u", refs, pp->num_ref_frames);
    slices = slice_pairs(c, sizeof(VASliceParameterBufferH264));
    if (slices < 0)
        return VA_STATUS_ERROR_INVALID_PARAMETER;
    for (i = 0; i < slices; i++) {
        VASliceParameterBufferH264 *sp = (VASliceParameterBufferH264 *)find(c, VASliceParameterBufferType, i)->data;
        struct buffer *data = find(c, VASliceDataBufferType, i);
        unsigned type = data->data[0] & 0x1f, slice_type = sp->slice_type % 5;

        if (sp->slice_data_size != data->size || sp->slice_data_offset != 0 || sp->slice_data_flag != VA_SLICE_DATA_FLAG_ALL)
            return fail("h264 slice %d: size %u offset %u flag %u for %u bytes of data", i, sp->slice_data_size,
                        sp->slice_data_offset, sp->slice_data_flag, data->size);
        if ((data->data[0] & 0x80) || (type != 1 && type != 5))
            return fail("h264 slice %d: data does not start with a slice NAL unit header (%02x)", i, data->data[0]);
        if (sp->slice_data_bit_offset < 8 + 3 || sp->slice_data_bit_offset >= 8 * data->size)
            return fail("h264 slice %d: slice data at bit %u of %u bytes", i, sp->slice_data_bit_offset, data->size);
        if (type == 5 && slice_type != 2)
            return fail("h264 slice %d: slice_type %u in an IDR picture", i, sp->slice_type);
        for (j = 0; j < 2; j++) {
            VAPictureH264 *list = j ? sp->RefPicList1 : sp->RefPicList0;
            int k, want = 0;
            if (j == 0 && (slice_type == 0 || slice_type == 1))
                want = sp->num_ref_idx_l0_active_minus1 + 1;
            if (j == 1 && slice_type == 1)
                want = sp->num_ref_idx_l1_active_minus1 + 1;
            for (k = 0; k < want; k++) {
                int m;
                if (list[k].flags & VA_PICTURE_H264_INVALID)
                    return fail("h264 slice %d: RefPicList%d[%d] is empty (%d entries expected)", i, j, k, want);
                for (m = 0; m < 16; m++)
                    if (!(pp->ReferenceFrames[m].flags & VA_PICTURE_H264_INVALID) &&
                        pp->ReferenceFrames[m].picture_id == list[k].picture_id &&
                        pp->ReferenceFrames[m].TopFieldOrderCnt == list[k].TopFieldOrderCnt)
                        break;
                if (m == 16)
                    return fail("h264 slice %d: RefPicList%d[%d] (surface %#x, POC %d) is not in ReferenceFrames", i, j, k,
                                list[k].picture_id, list[k].TopFieldOrderCnt);
            }
        }
    }
    paint(get_surface(c->target), pp->CurrPic.TopFieldOrderCnt);
    logf_("dec codec=h264 poc=%d surface=%u slices=%d refs=%d frame_num=%u", pp->CurrPic.TopFieldOrderCnt,
          c->target - SURFACE_BASE, slices, refs, pp->frame_num);
    return VA_STATUS_SUCCESS;
}

/* ---- AV1 ---- */

/* tg_start and tg_end are deprecated in libva but still sent by clients
 * and read by drivers. */
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"

static uint64_t fnv(uint64_t h, const void *data, size_t n)
{
    const uint8_t *p = data;
    size_t i;
    for (i = 0; i < n; i++)
        h = (h ^ p[i]) * 0x100000001b3ull;
    return h;
}

static void hex(char *dst, const void *data, size_t n)
{
    static const char digits[] = "0123456789abcdef";
    const uint8_t *p = data;
    size_t i;
    for (i = 0; i < n; i++) {
        dst[2 * i] = digits[p[i] >> 4];
        dst[2 * i + 1] = digits[p[i] & 15];
    }
    dst[2 * n] = 0;
}

/* picture_number returns the number of the picture in a surface, for the
 * log: surface identifiers differ between clients, picture numbers do not. */
static uint32_t picture_number(VASurfaceID id)
{
    struct surface *s = get_surface(id);
    if (id == VA_INVALID_SURFACE)
        return 0xffffffffu;
    return s ? (uint32_t)s->poc : 0xfffffffeu;
}

static VAStatus decode_av1(struct context *c)
{
    struct buffer *pb = find(c, VAPictureParameterBufferType, 0);
    VADecPictureParameterBufferAV1 *pp, norm;
    struct surface *display, *recon;
    int i, j, n = c->av1_pictures, key_shown, intra, grain, tiles = 0, groups = 0, want_data = 0;
    unsigned frame_type, sb, frame_w, cols = 0, rows = 0, denom, last_end = 0, data_size = 0;
    uint64_t tile_hash = 0xcbf29ce484222325ull, data_hash = 0xcbf29ce484222325ull;
    static char pp_hex[2 * sizeof(VADecPictureParameterBufferAV1) + 1];
    static char tile_hex[2 * 20 * 4096 + 1];
    size_t tile_hex_len = 0;

    if (!pb || count(c, VAPictureParameterBufferType) != 1)
        return fail("av1: need exactly one picture parameter buffer");
    if (pb->size != sizeof *pp || pb->num != 1)
        return fail("av1: picture parameter buffer has %u x %u bytes, VADecPictureParameterBufferAV1 has %zu", pb->size, pb->num, sizeof *pp);
    pp = (VADecPictureParameterBufferAV1 *)pb->data;
    frame_type = pp->pic_info_fields.bits.frame_type;
    key_shown = frame_type == 0 && pp->pic_info_fields.bits.show_frame;
    intra = frame_type == 0 || frame_type == 2;
    grain = pp->film_grain_info.film_grain_info_fields.bits.apply_grain;

    /* The picture is rendered into the reference surface, as ffmpeg does;
     * the surface to show is named in the parameters. */
    if (pp->current_frame != c->target)
        return fail("av1: current_frame names surface %#x, the render target is %#x", pp->current_frame, c->target);
    recon = get_surface(c->target);
    display = get_surface(pp->current_display_picture);
    if (!display)
        return fail("av1: current_display_picture names surface %#x, which does not exist", pp->current_display_picture);
    if (c->num_targets && !is_target(c, pp->current_display_picture))
        return fail("av1: current_display_picture %#x is not a render target of the context", pp->current_display_picture);
    if (grain && pp->current_frame == pp->current_display_picture)
        return fail("av1: film grain is applied but the picture to show is the reference picture");
    if (!grain && pp->current_frame != pp->current_display_picture)
        return fail("av1: no film grain, but current_frame and current_display_picture differ");
    if (grain && !pp->seq_info_fields.fields.film_grain_params_present)
        return fail("av1: apply_grain without film_grain_params_present");
    if (pp->profile != 0 || pp->bit_depth_idx != 0 || pp->seq_info_fields.fields.mono_chrome ||
        !pp->seq_info_fields.fields.subsampling_x || !pp->seq_info_fields.fields.subsampling_y)
        return fail("av1: not Main profile 8-bit 4:2:0 (profile %u, bit_depth_idx %u)", pp->profile, pp->bit_depth_idx);
    if (pp->frame_width_minus1 + 1 > c->width || pp->frame_height_minus1 + 1 > c->height)
        return fail("av1: frame is %ux%u, the context %dx%d", pp->frame_width_minus1 + 1, pp->frame_height_minus1 + 1, c->width, c->height);
    if (pp->anchor_frames_num || pp->anchor_frames_list || pp->pic_info_fields.bits.large_scale_tile)
        return fail("av1: large scale tile fields are set");
    if (pp->primary_ref_frame > 7 || pp->tile_cols < 1 || pp->tile_rows < 1 || pp->tile_cols > 64 || pp->tile_rows > 64)
        return fail("av1: primary_ref_frame %u, %ux%u tiles", pp->primary_ref_frame, pp->tile_cols, pp->tile_rows);
    denom = pp->superres_scale_denominator;
    if (denom < 8 || denom > 16 || (denom != 8) != pp->pic_info_fields.bits.use_superres)
        return fail("av1: superres_scale_denominator %u with use_superres %u", denom, pp->pic_info_fields.bits.use_superres);

    /* The tile sizes must add up to the coded frame. */
    sb = pp->seq_info_fields.fields.use_128x128_superblock ? 128 : 64;
    frame_w = ((pp->frame_width_minus1 + 1) * 8 + denom / 2) / denom;
    for (i = 0; i < pp->tile_cols && i < 63; i++)
        cols += pp->width_in_sbs_minus_1[i] + 1;
    for (i = 0; i < pp->tile_rows && i < 63; i++)
        rows += pp->height_in_sbs_minus_1[i] + 1;
    if (pp->tile_cols < 64 && cols != (((frame_w + 7) & ~7u) + sb - 1) / sb)
        return fail("av1: tile columns cover %u superblocks, the frame is %u samples wide (superblock %u)", cols, frame_w, sb);
    if (pp->tile_rows < 64 && rows != (((pp->frame_height_minus1 + 1 + 7) & ~7u) + sb - 1) / sb)
        return fail("av1: tile rows cover %u superblocks, the frame is %u samples high (superblock %u)", rows, pp->frame_height_minus1 + 1, sb);

    for (i = 0; i < 8; i++) {
        struct surface *r = get_surface(pp->ref_frame_map[i]);
        if (key_shown) {
            if (pp->ref_frame_map[i] != VA_INVALID_SURFACE)
                return fail("av1: shown key frame with a surface in reference slot %d", i);
            continue;
        }
        if (!r)
            return fail("av1: reference slot %d names surface %#x, which does not exist", i, pp->ref_frame_map[i]);
        if (r->poc == NO_POC)
            return fail("av1: reference slot %d names surface %#x, which was never decoded into", i, pp->ref_frame_map[i]);
        if (r->grainy)
            return fail("av1: reference slot %d holds a picture with film grain", i);
        if (pp->ref_frame_map[i] == pp->current_frame || pp->ref_frame_map[i] == pp->current_display_picture)
            return fail("av1: reference slot %d is the surface being decoded into", i);
    }
    if (!intra)
        for (i = 0; i < 7; i++)
            if (pp->ref_frame_idx[i] > 7)
                return fail("av1: ref_frame_idx[%d] = %u", i, pp->ref_frame_idx[i]);

    /* Tile groups: a parameter buffer with an element per tile, or one
     * parameter buffer per tile, each followed by the data it describes. */
    for (i = 0; i < c->num_bufs; i++) {
        struct buffer *b = get_buffer(c->bufs[i]), *data;
        VASliceParameterBufferAV1 *sp;
        if (b->type == VASliceDataBufferType) {
            if (!want_data)
                return fail("av1: slice data without slice parameters");
            want_data = 0;
            continue;
        }
        if (b->type != VASliceParameterBufferType)
            continue;
        if (want_data)
            return fail("av1: two slice parameter buffers without slice data between them");
        want_data = 1;
        if (b->size != sizeof *sp)
            return fail("av1: slice parameter elements have %u bytes, VASliceParameterBufferAV1 has %zu", b->size, sizeof *sp);
        if (i + 1 >= c->num_bufs || (data = get_buffer(c->bufs[i + 1]))->type != VASliceDataBufferType)
            return fail("av1: slice parameters without slice data");
        sp = (VASliceParameterBufferAV1 *)b->data;
        if (!groups || sp->slice_data_offset < last_end || data->size != data_size) {
            /* A new tile group (clients that send one tile per buffer
             * repeat the group's data). */
            groups++;
            last_end = 0;
            data_size = data->size;
            data_hash = fnv(data_hash, &data->size, sizeof data->size);
            data_hash = fnv(data_hash, data->data, data->size);
        }
        for (j = 0; j < (int)b->num; j++, sp++, tiles++) {
            unsigned idx = sp->tile_row * pp->tile_cols + sp->tile_column;
            uint8_t rec[20];
            if (sp->slice_data_flag != VA_SLICE_DATA_FLAG_ALL)
                return fail("av1 tile %d: slice_data_flag %u", tiles, sp->slice_data_flag);
            if (sp->tile_row >= pp->tile_rows || sp->tile_column >= pp->tile_cols || idx != (unsigned)tiles)
                return fail("av1 tile %d: row %u column %u of %ux%u tiles", tiles, sp->tile_row, sp->tile_column, pp->tile_cols, pp->tile_rows);
            if (sp->tg_start > idx || sp->tg_end < idx || sp->tg_end >= pp->tile_cols * pp->tile_rows)
                return fail("av1 tile %d: in tile group %u..%u", tiles, sp->tg_start, sp->tg_end);
            if (sp->slice_data_size == 0 || sp->slice_data_offset < last_end ||
                (uint64_t)sp->slice_data_offset + sp->slice_data_size > data->size)
                return fail("av1 tile %d: %u bytes at %u of %u, the previous tile ends at %u", tiles, sp->slice_data_size,
                            sp->slice_data_offset, data->size, last_end);
            if (idx == sp->tg_end && sp->slice_data_offset + sp->slice_data_size != data->size)
                return fail("av1 tile %d: the last tile of its group ends at %u of %u bytes", tiles,
                            sp->slice_data_offset + sp->slice_data_size, data->size);
            if (sp->anchor_frame_idx || sp->tile_idx_in_tile_list)
                return fail("av1 tile %d: large scale tile fields are set", tiles);
            last_end = sp->slice_data_offset + sp->slice_data_size;
            memcpy(rec, sp, sizeof rec); /* everything up to anchor_frame_idx */
            tile_hash = fnv(tile_hash, rec, sizeof rec);
            if (tile_hex_len + 2 * sizeof rec < sizeof tile_hex) {
                hex(tile_hex + tile_hex_len, rec, sizeof rec);
                tile_hex_len += 2 * sizeof rec;
            }
        }
    }
    if (want_data)
        return fail("av1: slice parameters without slice data");
    if (tiles != pp->tile_cols * pp->tile_rows)
        return fail("av1: %d tiles submitted, the frame has %ux%u", tiles, pp->tile_cols, pp->tile_rows);

    /* What the picture parameters say, with surfaces named by the number
     * of the picture they hold. */
    norm = *pp;
    norm.current_frame = 0;
    norm.current_display_picture = pp->current_frame != pp->current_display_picture;
    for (i = 0; i < 8; i++)
        norm.ref_frame_map[i] = picture_number(pp->ref_frame_map[i]);
    hex(pp_hex, &norm, sizeof norm);

    paint(recon, n);
    recon->grainy = 0;
    if (display != recon) {
        paint(display, n + 128);
        display->grainy = 1;
    }
    logf_("dec codec=av1 n=%d type=%u show=%u order_hint=%u width=%u height=%u recon=%u display=%u grain=%d tiles=%d groups=%d "
          "refs=%d,%d,%d,%d,%d,%d,%d,%d data=%016llx tp=%016llx pp=%s tiles_hex=%s",
          n, frame_type, pp->pic_info_fields.bits.show_frame, pp->order_hint, pp->frame_width_minus1 + 1, pp->frame_height_minus1 + 1,
          pp->current_frame - SURFACE_BASE, pp->current_display_picture - SURFACE_BASE, grain, tiles, groups,
          (int)norm.ref_frame_map[0], (int)norm.ref_frame_map[1], (int)norm.ref_frame_map[2], (int)norm.ref_frame_map[3],
          (int)norm.ref_frame_map[4], (int)norm.ref_frame_map[5], (int)norm.ref_frame_map[6], (int)norm.ref_frame_map[7],
          (unsigned long long)data_hash, (unsigned long long)tile_hash, pp_hex, tile_hex);
    c->av1_pictures++;
    return VA_STATUS_SUCCESS;
}

/* misc returns the misc parameter buffer of a type in the current picture. */
static void *misc(struct context *c, VAEncMiscParameterType type, unsigned payload)
{
    int i;
    for (i = 0; i < c->num_bufs; i++) {
        struct buffer *b = get_buffer(c->bufs[i]);
        if (b->type != VAEncMiscParameterBufferType)
            continue;
        if (((VAEncMiscParameterBuffer *)b->data)->type != type)
            continue;
        if (b->size != sizeof(VAEncMiscParameterBuffer) + payload) {
            fail("misc parameter %d has %u bytes, expected %zu", type, b->size, sizeof(VAEncMiscParameterBuffer) + payload);
            return NULL;
        }
        return ((VAEncMiscParameterBuffer *)b->data)->data;
    }
    return NULL;
}

/* packed returns the data of the packed header of a type, or NULL. */
static struct buffer *packed(struct context *c, unsigned type, unsigned *bits)
{
    int i;
    for (i = 0; i + 1 < c->num_bufs; i++) {
        struct buffer *b = get_buffer(c->bufs[i]), *d = get_buffer(c->bufs[i + 1]);
        VAEncPackedHeaderParameterBuffer *p;
        if (b->type != VAEncPackedHeaderParameterBufferType)
            continue;
        if (b->size != sizeof *p || d->type != VAEncPackedHeaderDataBufferType) {
            fail("packed header parameter buffer of %u bytes not followed by its data", b->size);
            return NULL;
        }
        p = (VAEncPackedHeaderParameterBuffer *)b->data;
        if (p->type != type)
            continue;
        if (p->bit_length == 0 || p->bit_length > 8 * d->size || p->bit_length + 7 < 8 * d->size) {
            fail("packed header type %u: %u bits in %u bytes", type, p->bit_length, d->size);
            return NULL;
        }
        if (!(env_int("FAKE_VA_PACKED", VA_ENC_PACKED_HEADER_SEQUENCE) & (type == VAEncPackedHeaderSequence ? VA_ENC_PACKED_HEADER_SEQUENCE : type == VAEncPackedHeaderSlice ? VA_ENC_PACKED_HEADER_SLICE : VA_ENC_PACKED_HEADER_PICTURE))) {
            fail("packed header type %u sent although the driver does not accept it", type);
            return NULL;
        }
        if (d->size < 5 || d->data[0] || d->data[1] || d->data[2] || d->data[3] != 1) {
            fail("packed header type %u does not begin with a start code", type);
            return NULL;
        }
        *bits = p->bit_length;
        return d;
    }
    return NULL;
}

static VAStatus encode(struct context *c, struct config *cfg)
{
    int hevc = is_hevc(cfg->profile);
    struct surface *in = get_surface(c->target), *recon;
    struct buffer *seq = find(c, VAEncSequenceParameterBufferType, 0);
    struct buffer *pic = find(c, VAEncPictureParameterBufferType, 0);
    struct buffer *slice = find(c, VAEncSliceParameterBufferType, 0);
    struct buffer *coded, *pseq, *pslice;
    unsigned seq_bits = 0, slice_bits = 0, sum = 0, n = 0, bps = 0, i;
    int idr, poc, qp, x, y;
    VASurfaceID recon_id, ref_id = VA_INVALID_SURFACE;
    int ref_poc = 0, have_ref = 0;
    uint8_t nal[2];
    VAStatus st;

    if (is_target(c, c->target))
        return fail("enc: the input surface is one of the reconstruction surfaces");
    if (!in->uploaded)
        return fail("enc: the input surface was not written since the previous picture");
    in->uploaded = 0;
    if (!pic || !slice || count(c, VAEncSliceParameterBufferType) != 1)
        return fail("enc: need one picture and one slice parameter buffer");

    if (hevc) {
        VAEncPictureParameterBufferHEVC *pp = (VAEncPictureParameterBufferHEVC *)pic->data;
        VAEncSliceParameterBufferHEVC *sp = (VAEncSliceParameterBufferHEVC *)slice->data;
        unsigned ctb, want;

        if (pic->size != sizeof *pp || slice->size != sizeof *sp)
            return fail("enc hevc: picture/slice buffers of %u/%u bytes, the structures have %zu/%zu", pic->size, slice->size, sizeof *pp, sizeof *sp);
        if (seq) {
            VAEncSequenceParameterBufferHEVC *s = (VAEncSequenceParameterBufferHEVC *)seq->data;
            if (seq->size != sizeof *s)
                return fail("enc hevc: sequence buffer of %u bytes, the structure has %zu", seq->size, sizeof *s);
            if (s->pic_width_in_luma_samples != c->width || s->pic_height_in_luma_samples != c->height)
                return fail("enc hevc: sequence is %ux%u, the context %dx%d", s->pic_width_in_luma_samples, s->pic_height_in_luma_samples, c->width, c->height);
            if (s->general_profile_idc != 1 || s->seq_fields.bits.chroma_format_idc != 1 || s->seq_fields.bits.bit_depth_luma_minus8)
                return fail("enc hevc: not Main 8-bit 4:2:0 (profile %u)", s->general_profile_idc);
            if (s->general_level_idc == 0 || s->general_level_idc % 3 || s->intra_period == 0 || s->ip_period != 1)
                return fail("enc hevc: level_idc %u intra_period %u ip_period %u", s->general_level_idc, s->intra_period, s->ip_period);
            c->ctb_log2 = s->log2_min_luma_coding_block_size_minus3 + 3 + s->log2_diff_max_min_luma_coding_block_size;
            if (c->ctb_log2 < 4 || c->ctb_log2 > 6)
                return fail("enc hevc: coding tree block size 2^%u", c->ctb_log2);
            if ((c->width | c->height) & ((1 << (s->log2_min_luma_coding_block_size_minus3 + 3)) - 1))
                return fail("enc hevc: %dx%d is not a multiple of the minimum coding block size", c->width, c->height);
            if (env_int("FAKE_VA_HEVC_ATTRS", 1) && c->ctb_log2 != 6)
                return fail("enc hevc: the driver reported 64x64 coding tree blocks, got 2^%u", c->ctb_log2);
            if (!env_int("FAKE_VA_HEVC_ATTRS", 1) && s->seq_fields.bits.sample_adaptive_offset_enabled_flag)
                return fail("enc hevc: SAO enabled without the driver offering it");
            bps = s->bits_per_second;
            c->have_seq = 1;
        }
        idr = pp->pic_fields.bits.idr_pic_flag;
        poc = pp->decoded_curr_pic.pic_order_cnt;
        qp = pp->pic_init_qp + sp->slice_qp_delta;
        recon_id = pp->decoded_curr_pic.picture_id;
        coded = get_buffer(pp->coded_buf);
        nal[0] = pp->nal_unit_type << 1;
        nal[1] = 1;
        if (!c->have_seq || (idr && !seq))
            return fail("enc hevc: IDR picture without sequence parameters");
        if (idr != (pp->nal_unit_type == 19 || pp->nal_unit_type == 20) || (!idr && pp->nal_unit_type != 1))
            return fail("enc hevc: nal_unit_type %u with idr_pic_flag %d", pp->nal_unit_type, idr);
        if (pp->pic_fields.bits.coding_type != (idr ? 1 : 2) || sp->slice_type != (idr ? 2 : 1))
            return fail("enc hevc: coding_type %u slice_type %u for idr %d", pp->pic_fields.bits.coding_type, sp->slice_type, idr);
        if (!pp->pic_fields.bits.reference_pic_flag || pp->collocated_ref_pic_index != 0xff)
            return fail("enc hevc: reference_pic_flag %u collocated_ref_pic_index %u", pp->pic_fields.bits.reference_pic_flag, pp->collocated_ref_pic_index);
        if (idr && poc != 0)
            return fail("enc hevc: IDR picture with POC %d", poc);
        for (i = 0; i < 15; i++) {
            VAPictureHEVC *r = &pp->reference_frames[i];
            if (r->flags & VA_PICTURE_HEVC_INVALID) {
                if (r->picture_id != VA_INVALID_SURFACE)
                    return fail("enc hevc: invalid reference %u has a surface", i);
                continue;
            }
            if (i != 0 || idr)
                return fail("enc hevc: unexpected reference %u (idr %d)", i, idr);
            ref_id = r->picture_id;
            ref_poc = r->pic_order_cnt;
            have_ref = 1;
            if (!(r->flags & VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE) || ref_poc != poc - 1)
                return fail("enc hevc: reference POC %d flags %#x for picture POC %d", ref_poc, r->flags, poc);
        }
        if (!idr && !have_ref)
            return fail("enc hevc: P picture without a reference");
        if (have_ref && (sp->ref_pic_list0[0].picture_id != ref_id || sp->ref_pic_list0[0].pic_order_cnt != ref_poc))
            return fail("enc hevc: ref_pic_list0[0] is not the reference frame");
        for (i = have_ref; i < 15; i++)
            if (!(sp->ref_pic_list0[i].flags & VA_PICTURE_HEVC_INVALID))
                return fail("enc hevc: ref_pic_list0[%u] is set", i);
        for (i = 0; i < 15; i++)
            if (!(sp->ref_pic_list1[i].flags & VA_PICTURE_HEVC_INVALID))
                return fail("enc hevc: ref_pic_list1[%u] is set", i);
        ctb = 1u << c->ctb_log2;
        want = ((c->width + ctb - 1) / ctb) * ((c->height + ctb - 1) / ctb);
        if (sp->num_ctu_in_slice != want || sp->slice_segment_address != 0 || !sp->slice_fields.bits.last_slice_of_pic_flag)
            return fail("enc hevc: slice of %u CTUs at %u (last %u), the picture has %u", sp->num_ctu_in_slice, sp->slice_segment_address,
                        sp->slice_fields.bits.last_slice_of_pic_flag, want);
        if (sp->max_num_merge_cand < 1 || sp->max_num_merge_cand > 5)
            return fail("enc hevc: max_num_merge_cand %u", sp->max_num_merge_cand);
        if (!pp->pic_fields.bits.cu_qp_delta_enabled_flag != (cfg->rc == VA_RC_CQP))
            return fail("enc hevc: cu_qp_delta_enabled_flag %u with rate control %#x", pp->pic_fields.bits.cu_qp_delta_enabled_flag, cfg->rc);
    } else {
        VAEncPictureParameterBufferH264 *pp = (VAEncPictureParameterBufferH264 *)pic->data;
        VAEncSliceParameterBufferH264 *sp = (VAEncSliceParameterBufferH264 *)slice->data;

        if (pic->size != sizeof *pp || slice->size != sizeof *sp)
            return fail("enc h264: picture/slice buffers of %u/%u bytes, the structures have %zu/%zu", pic->size, slice->size, sizeof *pp, sizeof *sp);
        if (seq) {
            VAEncSequenceParameterBufferH264 *s = (VAEncSequenceParameterBufferH264 *)seq->data;
            if (seq->size != sizeof *s)
                return fail("enc h264: sequence buffer of %u bytes, the structure has %zu", seq->size, sizeof *s);
            if (s->picture_width_in_mbs * 16 != c->width || s->picture_height_in_mbs * 16 != c->height)
                return fail("enc h264: sequence is %ux%u macroblocks, the context %dx%d", s->picture_width_in_mbs, s->picture_height_in_mbs, c->width, c->height);
            if (!s->seq_fields.bits.frame_mbs_only_flag || s->seq_fields.bits.chroma_format_idc != 1 || s->level_idc == 0 || s->intra_period == 0 || s->ip_period != 1)
                return fail("enc h264: sequence fields %#x level %u intra_period %u ip_period %u", s->seq_fields.value, s->level_idc, s->intra_period, s->ip_period);
            c->mbs = s->picture_width_in_mbs * s->picture_height_in_mbs;
            bps = s->bits_per_second;
            c->have_seq = 1;
        }
        idr = pp->pic_fields.bits.idr_pic_flag;
        poc = pp->CurrPic.TopFieldOrderCnt;
        qp = pp->pic_init_qp + sp->slice_qp_delta;
        recon_id = pp->CurrPic.picture_id;
        coded = get_buffer(pp->coded_buf);
        nal[0] = idr ? 0x65 : 0x41;
        if (!c->have_seq || (idr && !seq))
            return fail("enc h264: IDR picture without sequence parameters");
        if (sp->slice_type % 5 != (idr ? 2 : 0) || !pp->pic_fields.bits.reference_pic_flag)
            return fail("enc h264: slice_type %u reference_pic_flag %u for idr %d", sp->slice_type, pp->pic_fields.bits.reference_pic_flag, idr);
        if (idr && (poc != 0 || pp->frame_num != 0))
            return fail("enc h264: IDR picture with POC %d frame_num %u", poc, pp->frame_num);
        for (i = 0; i < 16; i++) {
            VAPictureH264 *r = &pp->ReferenceFrames[i];
            if (r->flags & VA_PICTURE_H264_INVALID)
                continue;
            if (i != 0 || idr)
                return fail("enc h264: unexpected reference %u (idr %d)", i, idr);
            ref_id = r->picture_id;
            ref_poc = r->TopFieldOrderCnt;
            have_ref = 1;
            if (!(r->flags & VA_PICTURE_H264_SHORT_TERM_REFERENCE) || ref_poc != poc - 2)
                return fail("enc h264: reference POC %d flags %#x for picture POC %d", ref_poc, r->flags, poc);
        }
        if (!idr && !have_ref)
            return fail("enc h264: P picture without a reference");
        if (have_ref && (sp->RefPicList0[0].picture_id != ref_id || sp->RefPicList0[0].TopFieldOrderCnt != ref_poc))
            return fail("enc h264: RefPicList0[0] is not the reference frame");
        if (sp->num_macroblocks != c->mbs || sp->macroblock_address != 0)
            return fail("enc h264: slice of %u macroblocks at %u, the picture has %u", sp->num_macroblocks, sp->macroblock_address, c->mbs);
    }

    recon = get_surface(recon_id);
    if (!recon || !is_target(c, recon_id) || recon_id == ref_id)
        return fail("enc: reconstruction surface %#x is not a free render target of the context", recon_id);
    if (have_ref && (st = check_reference("enc reference", 0, ref_id, ref_poc)) != VA_STATUS_SUCCESS)
        return st;
    if (qp < 0 || qp > 51)
        return fail("enc: QP %d", qp);
    if (!coded || coded->type != VAEncCodedBufferType)
        return fail("enc: coded_buf is not a coded buffer");
    if (cfg->rc != VA_RC_CQP && seq) {
        VAEncMiscParameterRateControl *rc = misc(c, VAEncMiscParameterTypeRateControl, sizeof *rc);
        VAEncMiscParameterHRD *hrd = misc(c, VAEncMiscParameterTypeHRD, sizeof *hrd);
        if (!rc || !hrd)
            return fail("enc: rate control %#x without rate control and HRD parameters", cfg->rc);
        if (rc->bits_per_second == 0 || rc->bits_per_second != bps || rc->target_percentage == 0 || rc->target_percentage > 100 ||
            (cfg->rc == VA_RC_CBR && rc->target_percentage != 100) || hrd->buffer_size == 0 || hrd->initial_buffer_fullness > hrd->buffer_size)
            return fail("enc: rate control %u bit/s (sequence %u) at %u%%, HRD %u/%u", rc->bits_per_second, bps, rc->target_percentage,
                        hrd->initial_buffer_fullness, hrd->buffer_size);
    }
    if (seq) {
        VAEncMiscParameterFrameRate *fr = misc(c, VAEncMiscParameterTypeFrameRate, sizeof *fr);
        if (fr && (fr->framerate & 0xffff) == 0)
            return fail("enc: frame rate %#x", fr->framerate);
    }

    /* The input: a checksum over the whole surface (padding included). */
    for (y = 0; y < in->height; y++)
        for (x = 0; x < in->width; x++)
            sum += in->data[y * in->pitch + x];
    for (y = 0; y < in->height / 2; y++)
        for (x = 0; x < in->width; x++)
            sum += chroma(in)[y * in->pitch + x];

    /* The output: the packed headers as given, then a fake slice. */
    pseq = packed(c, VAEncPackedHeaderSequence, &seq_bits);
    pslice = packed(c, VAEncPackedHeaderSlice, &slice_bits);
    if (pseq && !idr)
        return fail("enc: packed sequence header on a picture that is not IDR");
    if ((env_int("FAKE_VA_PACKED", VA_ENC_PACKED_HEADER_SEQUENCE) & VA_ENC_PACKED_HEADER_SLICE) && !pslice)
        return fail("enc: the driver needs packed slice headers and got none");
    if (pseq) {
        memcpy(coded->data + n, pseq->data, pseq->size);
        n += pseq->size;
    }
    if (pslice) {
        memcpy(coded->data + n, pslice->data, pslice->size);
        n += pslice->size;
    } else {
        static const uint8_t start[] = {0, 0, 0, 1};
        memcpy(coded->data + n, start, 4);
        n += 4;
        memcpy(coded->data + n, nal, hevc ? 2 : 1);
        n += hevc ? 2 : 1;
    }
    {
        static const uint8_t payload[] = {0xAB, 0xCD, 0xEF, 0x80};
        memcpy(coded->data + n, payload, sizeof payload);
        n += sizeof payload;
    }
    if (n > coded->size)
        return fail("enc: coded buffer of %u bytes is too small", coded->size);
    memset(&coded->segment, 0, sizeof coded->segment);
    coded->segment.size = n;
    coded->segment.buf = coded->data;

    recon->poc = poc;
    logf_("enc codec=%s n=%d idr=%d poc=%d qp=%d rc=%#x bps=%u in00=%u sum=%u seq=%d packed_seq=%d packed_slice=%d", hevc ? "hevc" : "h264",
          c->pictures, idr, poc, qp, cfg->rc, bps, in->data[0], sum, seq != NULL, pseq != NULL, pslice != NULL);
    c->pictures++;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_EndPicture(VADriverContextP ctx, VAContextID id)
{
    struct context *c = get_context(id);
    struct config *cfg;
    VAStatus st;

    if (!c)
        return VA_STATUS_ERROR_INVALID_CONTEXT;
    if (c->target == VA_INVALID_SURFACE)
        return fail("vaEndPicture without vaBeginPicture");
    cfg = &configs[c->config];
    if (is_encode(cfg->entrypoint))
        st = encode(c, cfg);
    else if (c->num_targets && !is_target(c, c->target))
        /* A context created without render targets (as ffmpeg does) takes
         * any surface. */
        st = fail("dec: surface %#x is not a render target of the context", c->target);
    else if (is_av1(cfg->profile))
        st = decode_av1(c);
    else if (is_hevc(cfg->profile))
        st = decode_hevc(c);
    else
        st = decode_h264(c);
    c->target = VA_INVALID_SURFACE;
    c->num_bufs = 0;
    return st;
}

static VAStatus fake_SyncSurface(VADriverContextP ctx, VASurfaceID id)
{
    return get_surface(id) ? VA_STATUS_SUCCESS : VA_STATUS_ERROR_INVALID_SURFACE;
}

static VAStatus fake_QuerySurfaceStatus(VADriverContextP ctx, VASurfaceID id, VASurfaceStatus *status)
{
    *status = VASurfaceReady;
    return VA_STATUS_SUCCESS;
}

/* ---- images ---- */

static VAStatus fake_QueryImageFormats(VADriverContextP ctx, VAImageFormat *list, int *num)
{
    memset(list, 0, sizeof *list);
    list[0].fourcc = VA_FOURCC_NV12;
    list[0].byte_order = VA_LSB_FIRST;
    list[0].bits_per_pixel = 12;
    *num = 1;
    return VA_STATUS_SUCCESS;
}

static VAStatus new_image(int width, int height, int pitch, int rows, int surface, VAImage *out)
{
    int i;
    VAStatus st;

    for (i = 0; i < MAX_IMAGES && images[i].used; i++)
        ;
    if (i == MAX_IMAGES)
        return fail("more than %d images alive: images are leaking", MAX_IMAGES);
    memset(&images[i], 0, sizeof images[i]);
    memset(out, 0, sizeof *out);
    out->image_id = IMAGE_BASE + i;
    out->format.fourcc = VA_FOURCC_NV12;
    out->format.byte_order = VA_LSB_FIRST;
    out->format.bits_per_pixel = 12;
    out->width = width;
    out->height = height;
    out->num_planes = 2;
    out->pitches[0] = out->pitches[1] = pitch;
    out->offsets[0] = 0;
    out->offsets[1] = pitch * rows;
    out->data_size = pitch * rows * 3 / 2;
    st = new_buffer(VAImageBufferType, out->data_size, 1, NULL, surface, &out->buf);
    if (st != VA_STATUS_SUCCESS)
        return st;
    images[i].used = 1;
    images[i].va = *out;
    images[i].derived = surface >= 0;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_CreateImage(VADriverContextP ctx, VAImageFormat *format, int width, int height, VAImage *image)
{
    if (format->fourcc != VA_FOURCC_NV12)
        return VA_STATUS_ERROR_INVALID_IMAGE_FORMAT;
    /* Its own layout, different from the surfaces'. */
    return new_image(width, height, width + 48, height + 4, -1, image);
}

static VAStatus fake_DeriveImage(VADriverContextP ctx, VASurfaceID id, VAImage *image)
{
    struct surface *s = get_surface(id);
    if (!s)
        return VA_STATUS_ERROR_INVALID_SURFACE;
    if (!env_int("FAKE_VA_DERIVE", 1))
        return VA_STATUS_ERROR_OPERATION_FAILED;
    return new_image(s->width, s->height, s->pitch, s->rows, id - SURFACE_BASE, image);
}

static VAStatus fake_DestroyImage(VADriverContextP ctx, VAImageID id)
{
    struct image *im = get_image(id);
    if (!im)
        return fail("vaDestroyImage: image %#x does not exist (destroyed twice?)", id);
    fake_DestroyBuffer(ctx, im->va.buf);
    im->used = 0;
    return VA_STATUS_SUCCESS;
}

/* copy moves the picture between a surface and an image of another layout. */
static VAStatus copy(struct surface *s, struct image *im, int to_surface, int width, int height)
{
    struct buffer *b = get_buffer(im->va.buf);
    uint8_t *sp[2] = {s->data, chroma(s)}, *ip[2] = {b->data, b->data + im->va.offsets[1]};
    int plane, y;

    if (im->derived)
        return fail("vaGetImage/vaPutImage with a derived image");
    if (width > s->width || height > s->height || width > im->va.width || height > im->va.height)
        return fail("vaGetImage/vaPutImage: %dx%d exceeds the surface (%dx%d) or the image (%ux%u)", width, height, s->width, s->height,
                    im->va.width, im->va.height);
    for (plane = 0; plane < 2; plane++)
        for (y = 0; y < (plane ? (height + 1) / 2 : height); y++) {
            uint8_t *a = sp[plane] + y * s->pitch, *c = ip[plane] + y * im->va.pitches[plane];
            if (to_surface)
                memcpy(a, c, width);
            else
                memcpy(c, a, width);
        }
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_GetImage(VADriverContextP ctx, VASurfaceID id, int x, int y, unsigned int width, unsigned int height, VAImageID image)
{
    struct surface *s = get_surface(id);
    struct image *im = get_image(image);
    if (!s || !im || x || y)
        return fail("vaGetImage: bad surface, image or origin");
    return copy(s, im, 0, width, height);
}

static VAStatus fake_PutImage(VADriverContextP ctx, VASurfaceID id, VAImageID image, int sx, int sy, unsigned int sw, unsigned int sh,
                              int dx, int dy, unsigned int dw, unsigned int dh)
{
    struct surface *s = get_surface(id);
    struct image *im = get_image(image);
    if (!s || !im || sx || sy || dx || dy || sw != dw || sh != dh)
        return fail("vaPutImage: bad surface, image or rectangles");
    s->uploaded = 1;
    return copy(s, im, 1, sw, sh);
}

/* ---- the rest of the mandatory table ---- */

static VAStatus fake_Terminate(VADriverContextP ctx)
{
    int i, leaked = 0;
    for (i = 0; i < MAX_BUFFERS; i++)
        leaked += buffers[i].used && buffers[i].type != VAImageBufferType && buffers[i].type != VAEncCodedBufferType;
    if (leaked)
        fail("vaTerminate with %d parameter buffers alive", leaked);
    return VA_STATUS_SUCCESS;
}

static VAStatus unimplemented(void) { return VA_STATUS_ERROR_UNIMPLEMENTED; }

static VAStatus fake_QuerySubpictureFormats(VADriverContextP ctx, VAImageFormat *list, unsigned int *flags, unsigned int *num)
{
    *num = 0;
    return VA_STATUS_SUCCESS;
}

static VAStatus fake_QueryDisplayAttributes(VADriverContextP ctx, VADisplayAttribute *list, int *num)
{
    *num = 0;
    return VA_STATUS_SUCCESS;
}

#define INIT_NAME_(major, minor) __vaDriverInit_##major##_##minor
#define INIT_NAME(major, minor) INIT_NAME_(major, minor)

VAStatus INIT_NAME(VA_MAJOR_VERSION, VA_MINOR_VERSION)(VADriverContextP ctx)
{
    struct VADriverVTable *v = ctx->vtable;

    ctx->version_major = VA_MAJOR_VERSION;
    ctx->version_minor = VA_MINOR_VERSION;
    ctx->max_profiles = 8;
    ctx->max_entrypoints = 4;
    ctx->max_attributes = 32;
    ctx->max_image_formats = 2;
    ctx->max_subpic_formats = 1;
    ctx->max_display_attributes = 1;
    ctx->str_vendor = "hwmediacodec fake VA driver";

    v->vaTerminate = fake_Terminate;
    v->vaQueryConfigProfiles = fake_QueryConfigProfiles;
    v->vaQueryConfigEntrypoints = fake_QueryConfigEntrypoints;
    v->vaGetConfigAttributes = fake_GetConfigAttributes;
    v->vaCreateConfig = fake_CreateConfig;
    v->vaDestroyConfig = fake_DestroyConfig;
    v->vaQueryConfigAttributes = fake_QueryConfigAttributes;
    v->vaCreateSurfaces = fake_CreateSurfaces;
    v->vaCreateSurfaces2 = fake_CreateSurfaces2;
    v->vaDestroySurfaces = fake_DestroySurfaces;
    v->vaQuerySurfaceAttributes = fake_QuerySurfaceAttributes;
    v->vaCreateContext = fake_CreateContext;
    v->vaDestroyContext = fake_DestroyContext;
    v->vaCreateBuffer = fake_CreateBuffer;
    v->vaBufferSetNumElements = fake_BufferSetNumElements;
    v->vaMapBuffer = fake_MapBuffer;
    v->vaUnmapBuffer = fake_UnmapBuffer;
    v->vaDestroyBuffer = fake_DestroyBuffer;
    v->vaBeginPicture = fake_BeginPicture;
    v->vaRenderPicture = fake_RenderPicture;
    v->vaEndPicture = fake_EndPicture;
    v->vaSyncSurface = fake_SyncSurface;
    v->vaQuerySurfaceStatus = fake_QuerySurfaceStatus;
    v->vaQueryImageFormats = fake_QueryImageFormats;
    v->vaCreateImage = fake_CreateImage;
    v->vaDeriveImage = fake_DeriveImage;
    v->vaDestroyImage = fake_DestroyImage;
    v->vaGetImage = fake_GetImage;
    v->vaPutImage = fake_PutImage;
    v->vaQuerySubpictureFormats = fake_QuerySubpictureFormats;
    v->vaQueryDisplayAttributes = fake_QueryDisplayAttributes;
    /* Never called by the backend; libva only requires them to be set. */
    v->vaSetImagePalette = (void *)unimplemented;
    v->vaCreateSubpicture = (void *)unimplemented;
    v->vaDestroySubpicture = (void *)unimplemented;
    v->vaSetSubpictureImage = (void *)unimplemented;
    v->vaSetSubpictureChromakey = (void *)unimplemented;
    v->vaSetSubpictureGlobalAlpha = (void *)unimplemented;
    v->vaAssociateSubpicture = (void *)unimplemented;
    v->vaDeassociateSubpicture = (void *)unimplemented;
    v->vaGetDisplayAttributes = (void *)unimplemented;
    v->vaSetDisplayAttributes = (void *)unimplemented;
    return VA_STATUS_SUCCESS;
}
