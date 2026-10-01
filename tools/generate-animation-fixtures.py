#!/usr/bin/env python3
"""Explicit development-only libwebpmux/WebPAnimDecoder oracle; never a Go backend."""
import argparse
import ctypes as C
import hashlib
import importlib.util
import json
from pathlib import Path
import struct

spec = importlib.util.spec_from_file_location('lossless_fixture_helper', Path(__file__).with_name('generate-lossless-fixtures.py'))
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)

class Data(C.Structure):
    _fields_ = [('bytes', C.c_void_p), ('size', C.c_size_t)]
class MuxFrame(C.Structure):
    _fields_ = [('bitstream', Data)] + [(x, C.c_int) for x in ['x', 'y', 'duration', 'id', 'dispose', 'blend']] + [('pad', C.c_uint32)]
class Params(C.Structure):
    _fields_ = [('background', C.c_uint32), ('loops', C.c_int)]
class Options(C.Structure):
    _fields_ = [('color', C.c_int), ('threads', C.c_int), ('pad', C.c_uint32 * 7)]
class Info(C.Structure):
    _fields_ = [(x, C.c_uint32) for x in ['width', 'height', 'loops', 'background', 'frames']] + [('pad', C.c_uint32 * 4)]

def bind(mux, demux):
    mux.WebPNewInternal.argtypes = [C.c_int]
    mux.WebPNewInternal.restype = C.c_void_p
    mux.WebPMuxDelete.argtypes = [C.c_void_p]
    mux.WebPMuxSetCanvasSize.argtypes = [C.c_void_p, C.c_int, C.c_int]
    mux.WebPMuxSetAnimationParams.argtypes = [C.c_void_p, C.POINTER(Params)]
    mux.WebPMuxPushFrame.argtypes = [C.c_void_p, C.POINTER(MuxFrame), C.c_int]
    mux.WebPMuxSetChunk.argtypes = [C.c_void_p, C.c_char_p, C.POINTER(Data), C.c_int]
    mux.WebPMuxAssemble.argtypes = [C.c_void_p, C.POINTER(Data)]
    demux.WebPAnimDecoderOptionsInitInternal.argtypes = [C.POINTER(Options), C.c_int]
    demux.WebPAnimDecoderNewInternal.argtypes = [C.POINTER(Data), C.POINTER(Options), C.c_int]
    demux.WebPAnimDecoderNewInternal.restype = C.c_void_p
    demux.WebPAnimDecoderGetInfo.argtypes = [C.c_void_p, C.POINTER(Info)]
    demux.WebPAnimDecoderHasMoreFrames.argtypes = [C.c_void_p]
    demux.WebPAnimDecoderGetNext.argtypes = [C.c_void_p, C.POINTER(C.c_void_p), C.POINTER(C.c_int)]
    demux.WebPAnimDecoderDelete.argtypes = [C.c_void_p]


def check(result, operation):
    if result != 1:
        raise RuntimeError(f'{operation} failed: {result}')

def assemble(lib, mux, width, height, loops, background, frames):
    handle = mux.WebPNewInternal(0x0109)
    if not handle:
        raise RuntimeError('mux creation failed')
    output = Data()
    try:
        check(mux.WebPMuxSetCanvasSize(handle, width, height), 'canvas')
        params = Params(background, loops)
        check(mux.WebPMuxSetAnimationParams(handle, C.byref(params)), 'animation parameters')
        for item in frames:
            buffer = C.create_string_buffer(item['encoded'])
            frame = MuxFrame(Data(C.cast(buffer, C.c_void_p), len(item['encoded'])), item['x'], item['y'], item['duration'], 3, int(item['dispose']), int(item['replace']), 0)
            check(mux.WebPMuxPushFrame(handle, C.byref(frame), 1), 'push frame')
        for tag, content in [(b'ICCP', b'profile'), (b'EXIF', b'late-exif'), (b'XMP ', b'xmp')]:
            buffer = C.create_string_buffer(content)
            data = Data(C.cast(buffer, C.c_void_p), len(content))
            check(mux.WebPMuxSetChunk(handle, tag, C.byref(data), 1), 'metadata')
        check(mux.WebPMuxAssemble(handle, C.byref(output)), 'assemble')
        if not output.bytes or not output.size:
            raise RuntimeError('empty assembled output')
        return C.string_at(output.bytes, output.size)
    finally:
        if output.bytes:
            lib.WebPFree(output.bytes)
        mux.WebPMuxDelete(handle)


def oracle(demux, encoded):
    options = Options()
    check(demux.WebPAnimDecoderOptionsInitInternal(C.byref(options), 0x0107), 'decoder ABI')
    options.color, options.threads = 1, 0
    buffer = C.create_string_buffer(encoded)
    data = Data(C.cast(buffer, C.c_void_p), len(encoded))
    decoder = demux.WebPAnimDecoderNewInternal(C.byref(data), C.byref(options), 0x0107)
    if not decoder:
        raise RuntimeError('animation decoder creation failed')
    try:
        info = Info()
        check(demux.WebPAnimDecoderGetInfo(decoder, C.byref(info)), 'animation info')
        if not info.width or not info.height or info.width * info.height > 1_000_000 or not 1 <= info.frames <= 1000:
            raise RuntimeError('oracle dimensions/count outside fixture policy')
        result = []
        for _ in range(info.frames):
            if not demux.WebPAnimDecoderHasMoreFrames(decoder):
                raise RuntimeError('early animation end')
            pixels, timestamp = C.c_void_p(), C.c_int()
            check(demux.WebPAnimDecoderGetNext(decoder, C.byref(pixels), C.byref(timestamp)), 'next animation frame')
            if not pixels.value:
                raise RuntimeError('missing canvas pixels')
            result.append((C.string_at(pixels, info.width * info.height * 4), timestamp.value))
        if demux.WebPAnimDecoderHasMoreFrames(decoder):
            raise RuntimeError('unexpected extra animation frame')
        return info, result
    finally:
        demux.WebPAnimDecoderDelete(decoder)


def chunks(data):
    if len(data) < 12 or data[:4] != b'RIFF' or data[8:12] != b'WEBP' or struct.unpack_from('<I', data, 4)[0] + 8 != len(data):
        raise ValueError('invalid RIFF')
    out, offset = [], 12
    while offset < len(data):
        if offset + 8 > len(data):
            raise ValueError('short chunk')
        size = struct.unpack_from('<I', data, offset + 4)[0]
        end = offset + 8 + size
        if end + (size & 1) > len(data):
            raise ValueError('chunk exceeds RIFF')
        out.append((data[offset:offset + 4], data[offset + 8:end]))
        offset = end + (size & 1)
    return out


def constant_lossless(width, height, rgba):
    bits = helper.Bits()
    for _ in range(3):
        bits.put(0, 1)  # no transform/cache/meta
    for value in [rgba[1], rgba[0], rgba[2], rgba[3], 0]:
        bits.put(1, 1); bits.put(0, 1); bits.put(1, 1); bits.put(value, 8)
    header = bytes([0x2f]) + struct.pack('<I', (width - 1) | ((height - 1) << 14) | (int(rgba[3] != 255) << 28))
    return helper.riff([helper.chunk(b'VP8L', header + bits.bytes())])


def neutral_lossy(lib, width, height, gray, alpha=255):
    pointer = C.c_void_p()
    try:
        raw = bytes([gray, gray, gray, alpha]) * (width * height)
        size = lib.WebPEncodeRGBA(raw, width, height, width * 4, 90, C.byref(pointer))
        if not size or not pointer.value:
            raise RuntimeError('neutral encode failed')
        return C.string_at(pointer, size)
    finally:
        if pointer.value:
            lib.WebPFree(pointer)


def frame(width, height, x, y, rgba, duration, replace=False, dispose=False):
    return dict(width=width, height=height, x=x, y=y, rgba=rgba, duration=duration, replace=replace, dispose=dispose, encoded=constant_lossless(width, height, rgba))


def cases(lib):
    a = [frame(3, 3, 2, 0, [90, 40, 200, 0], 0, dispose=True), frame(5, 3, 0, 0, [1, 200, 80, 3], 17), frame(3, 3, 2, 2, [240, 30, 10, 129], 33, dispose=True), frame(5, 5, 0, 0, [10, 90, 220, 65], 1), frame(7, 5, 0, 0, [31, 77, 99, 255], 23, replace=True)]
    b = [frame(5, 7, 0, 0, [20, 40, 60, 255], 17), frame(3, 3, 2, 2, [19, 57, 91, 0], 29, replace=True, dispose=True), frame(1, 3, 0, 4, [200, 80, 32, 180], 33)]
    c = [frame(5, 5, 0, 0, [80, 30, 250, 255], 17, dispose=True), frame(3, 3, 2, 0, [1, 2, 3, 3], 1, dispose=True), frame(1, 1, 2, 2, [19, 57, 91, 0], 33)]
    d = [frame(3, 3, 2, 2, [19, 57, 91, 0], 17), frame(1, 1, 0, 0, [1, 2, 3, 255], 1)]
    e = [frame(5, 7, 0, 0, [140, 140, 140, 255], 17), frame(3, 3, 2, 2, [20, 160, 230, 128], 33), frame(5, 7, 0, 0, [30, 30, 30, 97], 11)]
    e[0]['encoded'] = neutral_lossy(lib, 5, 7, 140)
    e[2]['encoded'] = neutral_lossy(lib, 5, 7, 30, 97)
    f = [frame(1, 1, 0, 0, [30, 40, 50, 255], 16777215), frame(1, 1, 2, 0, [90, 50, 20, 100], 0)]
    adversarial = []
    alphas = [0, 255, 1, 254, 2, 253, 3, 128, 127, 129, 64, 192]
    for index in range(64):
        full = index % 11 == 0
        width, height = (7, 5) if full else (3, 3)
        x, y = (0, 0) if full else (2 * (index % 3), 2 * ((index // 3) % 2))
        rgba = [[0, 1, 254, 255][index % 4], [255, 1, 0, 127][(index // 4) % 4], (37 * index) % 256, alphas[index % len(alphas)]]
        adversarial.append(frame(width, height, x, y, rgba, [0, 1, 17, 33][index % 4], replace=index % 5 == 0, dispose=index % 3 != 0))
    return [('blend-dispose', 7, 5, 0, a), ('replacement-transparency', 5, 7, 3, b), ('key-after-dispose', 5, 5, 1, c), ('one-frame-animation', 5, 5, 2, d), ('mixed-neutral', 5, 7, 1, e), ('max-duration', 3, 1, 65535, f), ('adversarial-alpha-sequence', 7, 5, 7, adversarial)]


def main(argv=None):
    parser = argparse.ArgumentParser()
    for name in ['library', 'mux-library', 'demux-library', 'out']:
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args(argv)
    lib = helper.bind_library(C.CDLL(args.library))
    mux, demux = C.CDLL(args.mux_library), C.CDLL(args.demux_library)
    bind(mux, demux)
    output = Path(args.out); output.mkdir(parents=True, exist_ok=True)
    records = []
    for name, width, height, loops, frames in cases(lib):
        encoded = assemble(lib, mux, width, height, loops, 0x7f123456, frames)
        revised = []
        seen_frame = 0
        for tag, data in chunks(encoded):
            if tag == b'ANMF':
                seen_frame += 1
                if name == 'one-frame-animation' and seen_frame > 1:
                    continue
                if name == 'blend-dispose' and seen_frame == 1:
                    data += helper.chunk(b'U001', b'first') + helper.chunk(b'U002', b'second')
            revised.append(helper.chunk(tag, data))
            if name == 'blend-dispose' and tag == b'ANMF' and seen_frame == 1:
                revised += [helper.chunk(b'EXIF', b'early-exif'), helper.chunk(b'TOPU', b'unknown')]
        encoded = helper.riff(revised)
        if name == 'one-frame-animation':
            frames = frames[:1]
        info, canvases = oracle(demux, encoded)
        if (info.width, info.height, info.frames) != (width, height, len(frames)):
            raise RuntimeError('native animation structure changed')
        (output / (name + '.webp')).write_bytes(encoded)
        entries = []
        for index, (item, (canvas, timestamp)) in enumerate(zip(frames, canvases)):
            base = f'{name}-{index:02d}'
            helper.save(lib, output, [], base + '.webp', item['encoded'], 'independent per-frame sample')
            (output / (base + '.canvas.rgba')).write_bytes(canvas)
            entry = {k: value for k, value in item.items() if k != 'encoded'}
            entry.update(sample_file=base + '.webp.rgba', encoded_file=base + '.webp', canvas_file=base + '.canvas.rgba', timestamp_ms=timestamp, canvas_sha256=hashlib.sha256(canvas).hexdigest())
            entries.append(entry)
        records.append(dict(file=name + '.webp', width=width, height=height, loop_count=info.loops, background=info.background, sha256=hashlib.sha256(encoded).hexdigest(), frames=entries))
    provenance = dict(libwebp=hex(lib.WebPGetDecoderVersion()), mux=hex(mux.WebPGetMuxVersion()), demux=hex(demux.WebPGetDemuxVersion()), oracle='WebPAnimDecoder MODE_RGBA, threads=0; neutral chroma only for VP8 frames; independent lossless literal streams/libwebpmux with documented unknown/one-frame framing mutations', fixtures=records)
    (output / 'provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')
    print(json.dumps({'animations': len(records), 'frames': sum(len(r['frames']) for r in records), 'versions': [provenance[k] for k in ['libwebp', 'mux', 'demux']]}))

if __name__ == '__main__':
    main()
