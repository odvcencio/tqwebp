"""Offline producer contracts; fake buffers/streams are not pixel-oracle evidence.

The checks exercise the actual producer, parse its RIFF/VP8L framing independently,
and reverse its ALPH filters. No installed libwebp or external dependency is used.
"""
import contextlib
import ctypes
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import struct
import tempfile
import unittest
from unittest import mock


def load_producer(filename):
    spec = importlib.util.spec_from_file_location(filename, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


generator = load_producer('generate-lossless-fixtures.py')
decode_generator = load_producer('generate-decode-fixtures.py')


def frame(kind, data):
    chunk = kind + struct.pack('<I', len(data)) + data + b'\0' * (len(data) % 2)
    return b'RIFF' + struct.pack('<I', 4 + len(chunk)) + b'WEBP' + chunk


class Function:
    def __init__(self, callback):
        self.callback = callback

    def __call__(self, *args):
        return self.callback(*args)


class Library:
    def __init__(self):
        self.init_status = 1
        self.decode_status = 0
        self.encode_status = True
        self.buffers = []
        self.decode_modes = []
        self.decode_frees = []
        self.encode_frees = []
        self.encode_inputs = []
        self.lossless_inputs = []
        self.lossless_outputs = []
        self.WebPInitDecoderConfigInternal = Function(self.initialize)
        self.WebPDecode = Function(self.decode)
        self.WebPFreeDecBuffer = Function(lambda ref: self.decode_frees.append(ref._obj.u.rgba.rgba))
        self.WebPEncodeRGBA = Function(self.encode)
        self.WebPEncodeLosslessRGBA = Function(self.lossless)
        self.WebPFree = Function(lambda ptr: self.encode_frees.append(ptr.value))
        self.WebPGetDecoderVersion = Function(lambda: 0x010500)

    def allocate(self, data):
        buffer = ctypes.create_string_buffer(data)
        self.buffers.append(buffer)
        return ctypes.addressof(buffer)

    def initialize(self, ref, abi):
        if abi != 0x0209:
            raise AssertionError('unexpected decoder ABI')
        return self.init_status

    def decode(self, data, size, ref):
        if size != len(data):
            raise AssertionError('unexpected input size')
        config = ref._obj
        self.decode_modes.append((config.output.mode, tuple(getattr(config.options, name) for name, _ in config.options._fields_ if name != 'pad')))
        config.output.width = config.output.height = 2
        config.output.u.rgba.stride = 12
        config.output.u.rgba.rgba = self.allocate(bytes(range(24)))
        return self.decode_status

    def encode(self, raw, width, height, stride, quality, ref):
        self.encode_inputs.append((raw, width, height, stride, quality))
        data = frame(b'VP8 ', b'encoded')
        ref._obj.value = self.allocate(data)
        return len(data) if self.encode_status else 0

    def lossless(self, raw, width, height, stride, ref):
        self.lossless_inputs.append((raw, width, height, stride))
        header = b'\x2f' + struct.pack('<I', (width - 1) | ((height - 1) << 14))
        # Deliberately nondecodable marker: permits exact framing checks without
        # pretending that a fake encoder establishes pixel equality.
        stream = b'\x8e' + struct.pack('<I', len(self.lossless_inputs)) + b'\xaa'
        data = frame(b'VP8L', header + stream)
        self.lossless_outputs.append((data, stream))
        ref._obj.value = self.allocate(data)
        return len(data) if self.encode_status else 0


class BitReader:
    def __init__(self, data):
        self.data = data
        self.position = 0

    def take(self, count):
        value = 0
        for bit in range(count):
            byte, offset = divmod(self.position, 8)
            value |= ((self.data[byte] >> offset) & 1) << bit
            self.position += 1
        return value


class LosslessFixtureContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.out = Path(self.temp.name)
        self.lib = generator.bind_library(Library())
        self.records = []

    def save(self):
        generator.save(self.lib, self.out, self.records, 'sample.webp', b'input', 'test producer')

    def generate(self):
        upstream = self.out / 'upstream'
        upstream.mkdir()
        for name in ['z.lossless.webp', 'a.lossless.webp', 'yellow_rose.lossy-with-alpha.webp']:
            (upstream / name).write_bytes(name.encode())
        output = self.out / 'result'
        with mock.patch.object(generator.C, 'CDLL', return_value=self.lib) as load, contextlib.redirect_stdout(io.StringIO()):
            generator.main(['--library', '/explicit/developer/libwebp.so', '--out', str(output), '--upstream', str(upstream)])
        load.assert_called_once_with('/explicit/developer/libwebp.so')
        return output

    def chunks(self, data):
        self.assertEqual(data[:4], b'RIFF')
        self.assertEqual(data[8:12], b'WEBP')
        self.assertEqual(struct.unpack_from('<I', data, 4)[0] + 8, len(data))
        result = []
        offset = 12
        while offset < len(data):
            self.assertGreaterEqual(len(data) - offset, 8)
            size = struct.unpack_from('<I', data, offset + 4)[0]
            end = offset + 8 + size
            self.assertLessEqual(end + (size & 1), len(data))
            result.append((data[offset:offset + 4], data[offset + 8:end]))
            if size & 1:
                self.assertEqual(data[end], 0)
            offset = end + (size & 1)
        self.assertEqual(offset, len(data))
        return result

    def simple_trees(self, stream):
        reader = BitReader(stream)
        self.assertEqual(reader.take(3), 0)  # no transforms, color cache or meta image
        trees = []
        for _ in range(5):
            self.assertEqual(reader.take(1), 1)
            count = reader.take(1) + 1
            width = 8 if reader.take(1) else 1
            symbols = [reader.take(width)]
            if count == 2:
                symbols.append(reader.take(8))
            trees.append(symbols)
        return trees, reader

    def test_import_is_offline_and_has_no_generation_side_effects(self):
        with mock.patch.object(ctypes, 'CDLL') as load, mock.patch.object(Path, 'write_bytes') as write:
            load_producer('generate-lossless-fixtures.py')
        load.assert_not_called()
        write.assert_not_called()

    def test_selected_library_is_required_before_loading(self):
        with mock.patch.object(generator.C, 'CDLL') as load, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                generator.main(['--out', str(self.out), '--upstream', str(self.out)])
        self.assertEqual(error.exception.code, 2)
        load.assert_not_called()
        self.assertEqual(list(self.out.iterdir()), [])

    def test_rgba_policy_stride_provenance_and_release(self):
        self.save()
        pixels = bytes(range(8)) + bytes(range(12, 20))
        self.assertEqual((self.out / 'sample.webp.rgba').read_bytes(), pixels)
        self.assertEqual(self.lib.decode_modes, [(1, (0, 1) + (0,) * 12)])
        self.assertEqual(len(self.lib.decode_frees), 1)
        self.assertEqual(self.records, [dict(file='sample.webp', source='test producer', width=2, height=2, sha256=hashlib.sha256(b'input').hexdigest(), rgba_sha256=hashlib.sha256(pixels).hexdigest())])

    def test_abi_mismatch_publishes_nothing(self):
        self.lib.init_status = 0
        with self.assertRaisesRegex(RuntimeError, 'ABI mismatch'):
            self.save()
        self.assertEqual(self.lib.decode_modes, [])
        self.assertEqual(self.lib.decode_frees, [])
        self.assertEqual(self.records, [])
        self.assertEqual(list(self.out.iterdir()), [])

    def test_decode_status_failure_releases_without_publishing(self):
        self.lib.decode_status = 3
        with self.assertRaisesRegex(RuntimeError, 'status 3'):
            self.save()
        self.assertEqual(len(self.lib.decode_frees), 1)
        self.assertEqual(self.records, [])
        self.assertEqual(list(self.out.iterdir()), [])

    def test_decode_call_exception_releases_allocated_buffer(self):
        failure = RuntimeError('decode call failed')
        def fail(*args):
            self.lib.decode(*args)
            raise failure
        self.lib.WebPDecode.callback = fail
        with self.assertRaises(RuntimeError) as error:
            self.save()
        self.assertIs(error.exception, failure)
        self.assertEqual(len(self.lib.decode_frees), 1)
        self.assertEqual(self.records, [])
        self.assertEqual(list(self.out.iterdir()), [])

    def test_pixel_copy_failure_releases_without_publishing(self):
        failure = ValueError('pixel copy failed')
        with mock.patch.object(generator.C, 'string_at', side_effect=failure):
            with self.assertRaises(ValueError) as error:
                self.save()
        self.assertIs(error.exception, failure)
        self.assertEqual(len(self.lib.decode_frees), 1)
        self.assertEqual(self.records, [])
        self.assertEqual(list(self.out.iterdir()), [])

    def test_each_output_write_failure_occurs_after_release(self):
        for fail_at in [1, 2]:
            with self.subTest(write=fail_at):
                failure = OSError('write failed')
                calls = []
                def write(data):
                    self.assertEqual(len(self.lib.decode_frees), fail_at)
                    calls.append(data)
                    if len(calls) == fail_at:
                        raise failure
                    return len(data)
                with mock.patch.object(Path, 'write_bytes', side_effect=write):
                    with self.assertRaises(OSError) as error:
                        self.save()
                self.assertIs(error.exception, failure)
                self.assertEqual(self.records, [])

    def test_lossless_encoder_input_and_success_release(self):
        raw = b'\x13\x39\x5b\0'
        data = generator.lossless(self.lib, raw, 1, 1)
        self.assertEqual(self.lib.lossless_inputs, [(raw, 1, 1, 4)])
        self.assertEqual(data, self.lib.lossless_outputs[0][0])
        self.assertEqual(len(self.lib.encode_frees), 1)
        self.assertEqual(self.lib.WebPEncodeLosslessRGBA.restype, ctypes.c_size_t)

    def test_encoder_status_and_copy_failure_release_owned_pointers(self):
        for name, call in [('lossy', lambda: generator.encode(self.lib, 1, 1)), ('lossless', lambda: generator.lossless(self.lib, b'\0' * 4, 1, 1))]:
            with self.subTest(encoder=name):
                before = len(self.lib.encode_frees)
                self.lib.encode_status = False
                with self.assertRaisesRegex(RuntimeError, 'encode failed'):
                    call()
                self.assertEqual(len(self.lib.encode_frees), before + 1)
                self.lib.encode_status = True
                failure = ValueError('encoded copy failed')
                with mock.patch.object(generator.C, 'string_at', side_effect=failure):
                    with self.assertRaises(ValueError) as error:
                        call()
                self.assertIs(error.exception, failure)
                self.assertEqual(len(self.lib.encode_frees), before + 2)

    def test_encoder_call_exceptions_release_for_both_producers(self):
        # Extends (without editing) the eight transferred host tests: an encoder
        # that sets its output pointer and then raises must still release it.
        for module in [decode_generator, generator]:
            encoders = [('WebPEncodeRGBA', self.lib.encode, lambda: module.encode(self.lib, 1, 1))]
            if module is generator:
                encoders.append(('WebPEncodeLosslessRGBA', self.lib.lossless, lambda: module.lossless(self.lib, b'\0' * 4, 1, 1)))
            for name, allocate, call in encoders:
                with self.subTest(producer=module.__name__, encoder=name):
                    before = len(self.lib.encode_frees)
                    failure = RuntimeError('encoder call failed')
                    def fail(*args):
                        allocate(*args)
                        raise failure
                    with mock.patch.object(getattr(self.lib, name), 'callback', side_effect=fail):
                        with self.assertRaises(RuntimeError) as error:
                            call()
                    self.assertIs(error.exception, failure)
                    self.assertEqual(len(self.lib.encode_frees), before + 1)

    def test_null_pointer_encode_failure_does_not_free_unowned_memory(self):
        for module in [decode_generator, generator]:
            encoders = [('WebPEncodeRGBA', lambda: module.encode(self.lib, 1, 1))]
            if module is generator:
                encoders.append(('WebPEncodeLosslessRGBA', lambda: module.lossless(self.lib, b'\0' * 4, 1, 1)))
            for name, call in encoders:
                with self.subTest(producer=module.__name__, encoder=name):
                    with mock.patch.object(getattr(self.lib, name), 'callback', return_value=0):
                        with self.assertRaisesRegex(RuntimeError, 'encode failed'):
                            call()
        self.assertEqual(self.lib.encode_frees, [])

    def test_generation_manifest_patterns_and_release_counts(self):
        output = self.generate()
        manifest = json.loads((output / 'provenance.json').read_text())
        self.assertEqual(manifest['libwebp_version'], '0x10500')
        self.assertEqual(manifest['oracle_mode'], 'MODE_RGBA; no_fancy_upsampling=1 (only affects VP8); filtering on; no dithering')
        self.assertEqual(len(manifest['fixtures']), 16)
        self.assertEqual([r['file'] for r in manifest['fixtures'][:3]], ['a.lossless.webp', 'z.lossless.webp', 'yellow_rose.lossy-with-alpha.webp'])
        self.assertEqual(len(list(output.iterdir())), 33)
        self.assertEqual(self.lib.decode_modes, [(1, (0, 1) + (0,) * 12)] * 16)
        self.assertEqual(len(self.lib.decode_frees), 16)
        self.assertEqual(len(self.lib.encode_frees), 10)
        self.assertEqual(len(self.lib.lossless_inputs), 9)
        self.assertEqual(len(self.lib.encode_inputs), 1)
        for record in manifest['fixtures']:
            self.assertEqual(record['sha256'], hashlib.sha256((output / record['file']).read_bytes()).hexdigest())
            self.assertEqual(record['rgba_sha256'], hashlib.sha256((output / (record['file'] + '.rgba')).read_bytes()).hexdigest())
        for args, dimensions in zip(self.lib.lossless_inputs[:5], [(1, 1), (1, 17), (17, 1), (19, 17), (257, 33)]):
            raw, width, height, stride = args
            self.assertEqual((width, height), dimensions)
            self.assertEqual(stride, width * 4)
            expected = bytes(value & 255 for y in range(height) for x in range(width) for value in (47 * x + 13 * y, 17 * x + 71 * y, 97 * x + 3 * y, 31 * x + 59 * y))
            self.assertEqual(raw, expected)

    def test_green_channel_alpha_filters_header_stripping_and_padding(self):
        output = self.generate()
        width, height = 19, 17
        expected = [(31 * x + 59 * y) & 255 for y in range(height) for x in range(width)]
        for mode in range(4):
            raw, actual_width, actual_height, stride = self.lib.lossless_inputs[5 + mode]
            self.assertEqual((actual_width, actual_height, stride), (width, height, width * 4))
            self.assertEqual(raw[0::4], bytes(width * height))
            self.assertEqual(raw[2::4], bytes(width * height))
            self.assertEqual(raw[3::4], b'\xff' * (width * height))
            actual = []
            for index, value in enumerate(raw[1::4]):
                y, x = divmod(index, width)
                left = actual[index - 1] if x else 0
                above = actual[index - width] if y else 0
                corner = actual[index - width - 1] if x and y else 0
                if mode == 0:
                    prediction = 0
                elif y == 0:
                    prediction = left
                elif x == 0 or mode == 2:
                    prediction = above
                elif mode == 1:
                    prediction = left
                else:
                    prediction = max(0, min(255, left + above - corner))
                actual.append((value + prediction) & 255)
            self.assertEqual(actual, expected)
            chunks = self.chunks((output / f'compressed-alpha-filter-{mode}.webp').read_bytes())
            self.assertEqual([kind for kind, _ in chunks], [b'VP8X', b'ALPH', b'VP8 '])
            self.assertEqual(chunks[0][1], bytes([16, 0, 0, 0, 18, 0, 0, 16, 0, 0]))
            self.assertEqual(chunks[1][1], bytes([1 | (mode << 2)]) + self.lib.lossless_outputs[5 + mode][1])
            self.assertEqual(chunks[2][1], b'encoded')

    def test_hidden_rgb_and_axis_boundary_simple_tree_framing(self):
        output = self.generate()
        for name, width, height in [('hidden-rgb.webp', 1, 1), ('lossless-boundary-16384x1.webp', 16384, 1), ('lossless-boundary-1x16384.webp', 1, 16384)]:
            with self.subTest(fixture=name):
                chunks = self.chunks((output / name).read_bytes())
                self.assertEqual(len(chunks), 1)
                kind, data = chunks[0]
                self.assertEqual(kind, b'VP8L')
                self.assertEqual(data[0], 0x2f)
                self.assertEqual(struct.unpack_from('<I', data, 1)[0], (width - 1) | ((height - 1) << 14) | (1 << 28))
                trees, reader = self.simple_trees(data[5:])
                self.assertEqual(trees, [[57], [19], [91], [0], [0]])
                self.assertEqual(reader.position, 58)
                self.assertEqual(reader.take(6), 0)  # only trailing byte padding
                self.assertEqual(len(data), 13)

    def test_duplicate_simple_symbols_preserve_zero_bit_alignment(self):
        output = self.generate()
        chunks = self.chunks((output / 'duplicate-simple-alignment.webp').read_bytes())
        self.assertEqual(len(chunks), 1)
        kind, data = chunks[0]
        self.assertEqual(kind, b'VP8L')
        self.assertEqual(data[:5], b'\x2f\x03\0\0\0')
        trees, reader = self.simple_trees(data[5:])
        self.assertEqual(trees, [[57, 57], [19, 201], [91], [255], [0]])
        self.assertEqual(reader.position, 74)
        pixels = []
        for _ in range(4):
            channels = []
            for symbols in trees[:4]:
                unique = sorted(set(symbols))
                channels.append(unique[reader.take(1)] if len(unique) == 2 else unique[0])
            green, red, blue, alpha = channels
            pixels.append((red, green, blue, alpha))
        self.assertEqual(pixels, [(19, 57, 91, 255), (201, 57, 91, 255)] * 2)
        self.assertEqual(reader.position, 78)
        self.assertEqual(reader.take(2), 0)
        self.assertEqual(len(data), 15)

    def test_provenance_write_failure_occurs_after_all_native_releases(self):
        failure = OSError('provenance write failed')
        with mock.patch.object(Path, 'write_text', side_effect=failure):
            with self.assertRaises(OSError) as error:
                self.generate()
        self.assertIs(error.exception, failure)
        self.assertEqual(len(self.lib.decode_frees), 16)
        self.assertEqual(len(self.lib.encode_frees), 10)
        self.assertFalse((self.out / 'result' / 'provenance.json').exists())

    def test_payload_extraction_skips_odd_padding_and_reports_missing_kind(self):
        data = generator.riff([generator.chunk(b'JUNK', b'x'), generator.chunk(b'VP8L', b'payload')])
        self.assertEqual(self.chunks(data), [(b'JUNK', b'x'), (b'VP8L', b'payload')])
        self.assertEqual(generator.payload(data, b'VP8L'), b'payload')
        with self.assertRaises(ValueError):
            generator.payload(data, b'VP8 ')


if __name__ == '__main__':
    unittest.main()
