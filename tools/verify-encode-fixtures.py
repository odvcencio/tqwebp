#!/usr/bin/env python3
"""Explicit development oracle for public tqwebp encodes; no production backend."""
import argparse
import ctypes as C
import hashlib
import importlib.util
import json
from pathlib import Path

spec = importlib.util.spec_from_file_location('animation_helper', Path(__file__).with_name('generate-animation-fixtures.py'))
animation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(animation)

def verify(lib, demux, directory):
    directory = Path(directory)
    cases = json.loads((directory / 'inputs.json').read_text())
    results = []
    for case in cases:
        encoded = (directory / (case['name'] + '.webp')).read_bytes()
        if case['animated']:
            info, frames = animation.oracle(demux, encoded)
            if (info.width, info.height, info.frames, info.loops) != (case['width'], case['height'], len(case['frames']), case['loops']):
                raise RuntimeError('animation control mismatch')
        else:
            records = []
            name = case['name'] + '.webp'
            animation.helper.save(lib, directory, records, name, encoded, 'tqwebp public encoder')
            width, height = records[0]['width'], records[0]['height']
            raw = (directory / (name + '.rgba')).read_bytes()
            if (width, height) != (case['width'], case['height']):
                raise RuntimeError('still dimensions mismatch')
            frames = [(raw, 0)]
        if len(frames) != len(case['frames']):
            raise RuntimeError('frame count mismatch')
        timestamp = 0
        for index, (control, (pixels, actual_timestamp)) in enumerate(zip(case['frames'], frames)):
            source = (directory / control['source']).read_bytes()
            timestamp += control['duration_ms']
            if len(pixels) != len(source) or pixels[3::4] != source[3::4]:
                raise RuntimeError('independent alpha mismatch')
            if actual_timestamp != timestamp:
                raise RuntimeError('animation timestamp mismatch')
            name = f"{case['name']}-{index:02d}.oracle.rgba"
            (directory / name).write_bytes(pixels)
            results.append(dict(file=name, sha256=hashlib.sha256(pixels).hexdigest(), timestamp_ms=timestamp))
    provenance = dict(libwebp=hex(lib.WebPGetDecoderVersion()), oracle='WebPAnimDecoder MODE_RGBA for animation; no-fancy libwebp RGBA for stills; neutral-chroma samples', frames=results)
    (directory / 'oracle.json').write_text(json.dumps(provenance, indent=2) + '\n')
    return provenance

def main(argv=None):
    p = argparse.ArgumentParser()
    for name in ['library', 'demux-library', 'input']:
        p.add_argument('--' + name, required=True)
    args = p.parse_args(argv)
    lib = animation.helper.bind_library(C.CDLL(args.library))
    demux = C.CDLL(args.demux_library)
    demux.WebPAnimDecoderOptionsInitInternal.argtypes = [C.POINTER(animation.Options), C.c_int]
    demux.WebPAnimDecoderNewInternal.argtypes = [C.POINTER(animation.Data), C.POINTER(animation.Options), C.c_int]
    demux.WebPAnimDecoderNewInternal.restype = C.c_void_p
    demux.WebPAnimDecoderGetInfo.argtypes = [C.c_void_p, C.POINTER(animation.Info)]
    demux.WebPAnimDecoderHasMoreFrames.argtypes = [C.c_void_p]
    demux.WebPAnimDecoderGetNext.argtypes = [C.c_void_p, C.POINTER(C.c_void_p), C.POINTER(C.c_int)]
    demux.WebPAnimDecoderDelete.argtypes = [C.c_void_p]
    result = verify(lib, demux, args.input)
    print(json.dumps({'frames': len(result['frames']), 'libwebp': result['libwebp']}))

if __name__ == '__main__':
    main()
