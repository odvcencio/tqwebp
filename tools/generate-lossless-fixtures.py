#!/usr/bin/env python3
"""Explicit offline developer oracle; never invoked by the Go library.

Uses a user-selected libwebp shared library, ABI 0x0209, no fancy upsampling.
"""
import argparse
import ctypes as C
import hashlib
import json
import pathlib
import struct


class RGBA(C.Structure):
    _fields_ = [('rgba', C.c_void_p), ('stride', C.c_int), ('size', C.c_size_t)]


class YUVA(C.Structure):
    _fields_ = [(s, C.c_void_p) for s in ['y', 'u', 'v', 'a']] + [(s, C.c_int) for s in ['ys', 'us', 'vs', 'as_']] + [(s, C.c_size_t) for s in ['yz', 'uz', 'vz', 'az']]


class U(C.Union):
    _fields_ = [('rgba', RGBA), ('yuva', YUVA)]


class Buffer(C.Structure):
    _fields_ = [('mode', C.c_int), ('width', C.c_int), ('height', C.c_int), ('external', C.c_int), ('u', U), ('pad', C.c_uint32 * 4), ('private', C.c_void_p)]


class Features(C.Structure):
    _fields_ = [(s, C.c_int) for s in ['w', 'h', 'alpha', 'anim', 'format']] + [('pad', C.c_uint32 * 5)]


class Options(C.Structure):
    _fields_ = [(s, C.c_int) for s in ['bypass', 'no_fancy', 'crop', 'left', 'top', 'width', 'height', 'scale', 'sw', 'sh', 'threads', 'dither', 'flip', 'alpha_dither']] + [('pad', C.c_uint32 * 5)]


class Config(C.Structure):
    _fields_ = [('input', Features), ('output', Buffer), ('options', Options)]


def bind_library(lib):
    lib.WebPInitDecoderConfigInternal.argtypes = [C.POINTER(Config), C.c_int]
    lib.WebPDecode.argtypes = [C.c_void_p, C.c_size_t, C.POINTER(Config)]
    lib.WebPFreeDecBuffer.argtypes = [C.POINTER(Buffer)]
    lib.WebPEncodeRGBA.argtypes = [C.c_void_p, C.c_int, C.c_int, C.c_int, C.c_float, C.POINTER(C.c_void_p)]
    lib.WebPEncodeRGBA.restype = C.c_size_t
    lib.WebPFree.argtypes = [C.c_void_p]
    lib.WebPEncodeLosslessRGBA.argtypes = [C.c_void_p, C.c_int, C.c_int, C.c_int, C.POINTER(C.c_void_p)]
    lib.WebPEncodeLosslessRGBA.restype = C.c_size_t
    return lib


def save(lib, out, records, name, payload, source):
    config = Config()
    if lib.WebPInitDecoderConfigInternal(C.byref(config), 0x0209) != 1:
        raise RuntimeError('libwebp decoder ABI mismatch')
    config.output.mode = 1  # MODE_RGBA
    config.options.no_fancy = 1
    try:
        status = lib.WebPDecode(payload, len(payload), C.byref(config))
        if status != 0:
            raise RuntimeError(f'libwebp decode failed for {name}: status {status}')
        pixels = b''.join(C.string_at(config.output.u.rgba.rgba + y * config.output.u.rgba.stride, config.output.width * 4) for y in range(config.output.height))
        width, height = config.output.width, config.output.height
    finally:
        lib.WebPFreeDecBuffer(C.byref(config.output))
    (out / name).write_bytes(payload)
    (out / (name + '.rgba')).write_bytes(pixels)
    records.append(dict(file=name, source=source, width=width, height=height, sha256=hashlib.sha256(payload).hexdigest(), rgba_sha256=hashlib.sha256(pixels).hexdigest()))


def encode(lib, width, height, alpha=False):
    raw = bytes(c for y in range(height) for x in range(width) for c in ((x * 47 + y * 13) % 256, (x * 17 + y * 71) % 256, (x * 97 + y * 3) % 256, (x * 31 + y * 59) % 256 if alpha else 255))
    ptr = C.c_void_p()
    try:
        size = lib.WebPEncodeRGBA(raw, width, height, width * 4, 83, C.byref(ptr))
        if not size:
            raise RuntimeError('libwebp encode failed')
        return C.string_at(ptr, size)
    finally:
        if ptr.value:
            lib.WebPFree(ptr)


def chunk(kind, data):
    return kind + struct.pack('<I', len(data)) + data + (b'\0' if len(data) & 1 else b'')


def riff(chunks):
    body = b'WEBP' + b''.join(chunks)
    return b'RIFF' + struct.pack('<I', len(body)) + body


def lossless(lib, raw, width, height):
    ptr = C.c_void_p()
    try:
        size = lib.WebPEncodeLosslessRGBA(raw, width, height, width * 4, C.byref(ptr))
        if not size:
            raise RuntimeError('libwebp lossless encode failed')
        return C.string_at(ptr, size)
    finally:
        if ptr.value:
            lib.WebPFree(ptr)


def payload(data, kind):
    offset = 12
    while offset < len(data):
        size = struct.unpack('<I', data[offset + 4:offset + 8])[0]
        if data[offset:offset + 4] == kind:
            return data[offset + 8:offset + 8 + size]
        offset += 8 + size + (size & 1)
    raise ValueError(kind)


class Bits:
    def __init__(self):
        self.bits = []

    def put(self, value, count):
        self.bits.extend((value >> i) & 1 for i in range(count))

    def bytes(self):
        return bytes(sum(self.bits[k + i] << i for i in range(min(8, len(self.bits) - k))) for k in range(0, len(self.bits), 8))


def main(argv=None):
    parser = argparse.ArgumentParser()
    for option in ['library', 'out', 'upstream']:
        parser.add_argument('--' + option, required=True)
    args = parser.parse_args(argv)
    lib = bind_library(C.CDLL(args.library))
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    records = []
    for path in sorted(pathlib.Path(args.upstream).glob('*.lossless.webp')):
        save(lib, out, records, path.name, path.read_bytes(), 'golang.org/x/image v0.38.0/testdata/' + path.name)
    path = pathlib.Path(args.upstream) / 'yellow_rose.lossy-with-alpha.webp'
    save(lib, out, records, path.name, path.read_bytes(), 'golang.org/x/image v0.38.0/testdata/' + path.name)
    for width, height in [(1, 1), (1, 17), (17, 1), (19, 17), (257, 33)]:
        raw = bytes(c for y in range(height) for x in range(width) for c in ((x * 47 + y * 13) % 256, (x * 17 + y * 71) % 256, (x * 97 + y * 3) % 256, (x * 31 + y * 59) % 256))
        save(lib, out, records, f'lossless-pattern-{width}x{height}.webp', lossless(lib, raw, width, height), 'libwebp WebPEncodeLosslessRGBA deterministic RGBA pattern')
    width, height = 19, 17
    vp8 = payload(encode(lib, width, height), b'VP8 ')
    alpha = [(x * 31 + y * 59) % 256 for y in range(height) for x in range(width)]
    for filter_id in range(4):
        residuals = []
        for y in range(height):
            for x in range(width):
                prediction = 0
                if filter_id:
                    if y == 0:
                        prediction = alpha[y * width + x - 1] if x else 0
                    elif x == 0:
                        prediction = alpha[(y - 1) * width]
                    elif filter_id == 1:
                        prediction = alpha[y * width + x - 1]
                    elif filter_id == 2:
                        prediction = alpha[(y - 1) * width + x]
                    else:
                        prediction = max(0, min(255, alpha[y * width + x - 1] + alpha[(y - 1) * width + x] - alpha[(y - 1) * width + x - 1]))
                residuals.append((alpha[y * width + x] - prediction) % 256)
        raw = bytes(c for green in residuals for c in (0, green, 0, 255))
        compressed = payload(lossless(lib, raw, width, height), b'VP8L')[5:]
        data = riff([chunk(b'VP8X', bytes([16, 0, 0, 0]) + (width - 1).to_bytes(3, 'little') + (height - 1).to_bytes(3, 'little')), chunk(b'ALPH', bytes([1 | (filter_id << 2)]) + compressed), chunk(b'VP8 ', vp8)])
        save(lib, out, records, f'compressed-alpha-filter-{filter_id}.webp', data, 'Independently generated green-channel residuals; VP8L header omitted per ALPH specification; libwebp oracle validates complete container')
    # Independently specified one-symbol trees encode hidden RGB exactly. No image
    # encoder may canonicalize transparent RGB before this decoder regression.
    bits = Bits()
    bits.put(0, 1)
    bits.put(0, 1)
    bits.put(0, 1)
    for value in [57, 19, 91, 0, 0]:
        bits.put(1, 1)
        bits.put(0, 1)
        bits.put(1, 1)
        bits.put(value, 8)
    vp8l = bytes([0x2f]) + struct.pack('<I', 1 << 28) + bits.bytes()
    save(lib, out, records, 'hidden-rgb.webp', riff([chunk(b'VP8L', vp8l)]), 'Specification-built one-symbol Huffman trees: RGBA=(19,57,91,0), libwebp independently validates hidden RGB')
    for width, height in [(16384, 1), (1, 16384)]:
        header = bytes([0x2f]) + struct.pack('<I', (width - 1) | ((height - 1) << 14) | (1 << 28))
        save(lib, out, records, f'lossless-boundary-{width}x{height}.webp', riff([chunk(b'VP8L', header + bits.bytes())]), 'Specification-built constant hidden-RGB image at the VP8L 16384-axis boundary; libwebp independently validates')
    # Duplicate simple symbols are a valid zero-bit singleton. Distinct red
    # symbols consume the following bits, proving alignment across all four pixels.
    bits = Bits()
    bits.put(0, 1)
    bits.put(0, 1)
    bits.put(0, 1)
    for values in [(57, 57), (19, 201), (91,), (255,), (0,)]:
        bits.put(1, 1)
        bits.put(len(values) - 1, 1)
        bits.put(1, 1)
        bits.put(values[0], 8)
        if len(values) == 2:
            bits.put(values[1], 8)
    for bit in [0, 1, 0, 1]:
        bits.put(bit, 1)
    header = bytes([0x2f]) + struct.pack('<I', 3)
    save(lib, out, records, 'duplicate-simple-alignment.webp', riff([chunk(b'VP8L', header + bits.bytes())]), 'Specification-built permitted duplicate simple symbols; alternating red bits prove zero-bit singleton alignment; independently decoded by libwebp')
    version = hex(lib.WebPGetDecoderVersion())
    (out / 'provenance.json').write_text(json.dumps({'libwebp_version': version, 'oracle_mode': 'MODE_RGBA; no_fancy_upsampling=1 (only affects VP8); filtering on; no dithering', 'fixtures': records}, indent=2) + '\n')
    print(json.dumps({'version': version, 'fixtures': len(records)}))


if __name__ == '__main__':
    main()
