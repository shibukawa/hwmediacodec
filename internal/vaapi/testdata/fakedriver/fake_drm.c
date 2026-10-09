/*
 * fake_drm.c - lets libva open a display on a file that is not a DRM device.
 *
 * vaGetDisplayDRM() rejects file descriptors that libdrm does not recognise
 * as a DRM node, and a container without a GPU has none. Preloading this
 * library makes every descriptor look like a render node, so the tests can
 * hand libva /dev/null; the driver itself is then chosen with
 * LIBVA_DRIVER_NAME and never touches the descriptor.
 *
 * Build: gcc -shared -fPIC -o libfakedrm.so fake_drm.c
 * Use:   LD_PRELOAD=<dir>/libfakedrm.so
 */

/* DRM_NODE_RENDER of xf86drm.h. */
#define FAKE_DRM_NODE_RENDER 2

int drmGetNodeTypeFromFd(int fd)
{
    (void)fd;
    return FAKE_DRM_NODE_RENDER;
}
