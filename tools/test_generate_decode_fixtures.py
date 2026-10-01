"""Offline producer-contract tests; fake C buffers are not pixel-oracle evidence."""
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

spec = importlib.util.spec_from_file_location('fixture_generator', Path(__file__).with_name('generate-decode-fixtures.py'))
generator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generator)


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
        self.WebPInitDecoderConfigInternal = Function(self.initialize)
        self.WebPDecode = Function(self.decode)
        self.WebPFreeDecBuffer = Function(lambda ref: self.decode_frees.append(ref._obj.u.rgba.rgba))
        self.WebPEncodeRGBA = Function(self.encode)
        self.WebPFree = Function(lambda ptr: self.encode_frees.append(ptr.value))
        self.WebPGetDecoderVersion = Function(lambda: 0x010500)

    def allocate(self, payload):
        buffer = ctypes.create_string_buffer(payload)
        self.buffers.append(buffer)
        return ctypes.addressof(buffer)

    def initialize(self, ref, abi):
        if abi != 0x0209:
            raise AssertionError('unexpected decoder ABI')
        return self.init_status

    def decode(self, payload, size, ref):
        config = ref._obj
        self.decode_modes.append((config.output.mode, config.options.no_fancy, config.options.bypass, config.options.dither, config.options.alpha_dither))
        config.output.width = config.output.height = 2
        config.output.u.rgba.stride = 12  # four padding bytes per row
        config.output.u.rgba.rgba = self.allocate(bytes(range(24)))
        return self.decode_status

    def encode(self, raw, width, height, stride, quality, ref):
        self.encode_inputs.append((raw, width, height, stride, quality))
        payload = generator.riff([generator.chunk(b'VP8 ', b'encoded')])
        ref._obj.value = self.allocate(payload)
        return len(payload) if self.encode_status else 0


class FixtureContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.out = Path(self.temp.name)
        self.lib = generator.bind_library(Library())
        self.records = []

    def save(self):
        generator.save(self.lib, self.out, self.records, 'sample.webp', b'input', 'test producer')

    def test_selected_library_is_required_before_loading(self):
        with mock.patch.object(generator.C, 'CDLL') as load, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                generator.main(['--out', str(self.out), '--upstream', str(self.out)])
            self.assertEqual(error.exception.code, 2)
            load.assert_not_called()

    def test_rgba_policy_stride_and_provenance(self):
        self.save()
        pixels = bytes(range(8)) + bytes(range(12, 20))
        self.assertEqual((self.out / 'sample.webp.rgba').read_bytes(), pixels)
        self.assertEqual(self.lib.decode_modes, [(1, 1, 0, 0, 0)])
        self.assertEqual(len(self.lib.decode_frees), 1)
        self.assertEqual(self.records, [dict(file='sample.webp', source='test producer', width=2, height=2, sha256=hashlib.sha256(b'input').hexdigest(), rgba_sha256=hashlib.sha256(pixels).hexdigest())])

    def test_abi_mismatch_publishes_nothing(self):
        self.lib.init_status = 0
        with self.assertRaisesRegex(RuntimeError, 'ABI mismatch'):
            self.save()
        self.assertEqual(list(self.out.iterdir()), [])
        self.assertEqual(self.lib.decode_modes, [])
        self.assertEqual(self.records, [])

    def test_decode_failure_releases_buffer_without_publishing(self):
        self.lib.decode_status = 3
        with self.assertRaisesRegex(RuntimeError, 'status 3'):
            self.save()
        self.assertEqual(len(self.lib.decode_frees), 1)
        self.assertEqual(list(self.out.iterdir()), [])
        self.assertEqual(self.records, [])

    def test_pixel_copy_failure_releases_buffer(self):
        failure = ValueError('copy failed')
        with mock.patch.object(generator.C, 'string_at', side_effect=failure):
            with self.assertRaises(ValueError) as error:
                self.save()
        self.assertIs(error.exception, failure)
        self.assertEqual(len(self.lib.decode_frees), 1)
        self.assertEqual(list(self.out.iterdir()), [])

    def test_write_failure_preserves_error_after_free(self):
        failure = OSError('destination failed')
        with mock.patch.object(Path, 'write_bytes', side_effect=failure):
            with self.assertRaises(OSError) as error:
                self.save()
        self.assertIs(error.exception, failure)
        self.assertEqual(len(self.lib.decode_frees), 1)
        self.assertEqual(self.records, [])

    def test_encode_failure_and_copy_failure_release_owned_pointer(self):
        self.lib.encode_status = False
        with self.assertRaisesRegex(RuntimeError, 'encode failed'):
            generator.encode(self.lib, 1, 1)
        self.assertEqual(len(self.lib.encode_frees), 1)
        self.lib.encode_status = True
        with mock.patch.object(generator.C, 'string_at', side_effect=ValueError('copy')):
            with self.assertRaises(ValueError):
                generator.encode(self.lib, 1, 1)
        self.assertEqual(len(self.lib.encode_frees), 2)
        self.assertEqual(self.lib.encode_inputs[0], (bytes([0, 0, 0, 255]), 1, 1, 4, 83))

    def test_generation_manifest_and_all_alpha_filters(self):
        upstream = self.out / 'upstream'
        upstream.mkdir()
        for name in ['blue-purple-pink.lossy.webp', 'blue-purple-pink-large.no-filter.lossy.webp', 'blue-purple-pink-large.normal-filter.lossy.webp', 'blue-purple-pink-large.simple-filter.lossy.webp']:
            (upstream / name).write_bytes(b'upstream fixture')
        output = self.out / 'result'
        with mock.patch.object(generator.C, 'CDLL', return_value=self.lib) as load, contextlib.redirect_stdout(io.StringIO()):
            generator.main(['--library', '/explicit/developer/libwebp.so', '--out', str(output), '--upstream', str(upstream)])
        load.assert_called_once_with('/explicit/developer/libwebp.so')
        manifest = json.loads((output / 'provenance.json').read_text())
        self.assertEqual(manifest['libwebp_version'], '0x10500')
        self.assertEqual(len(manifest['fixtures']), 12)
        self.assertEqual(self.lib.decode_modes, [(1, 1, 0, 0, 0)] * 12)
        self.assertEqual(len(self.lib.decode_frees), 12)
        self.assertEqual(len(self.lib.encode_frees), 6)
        for record in manifest['fixtures']:
            self.assertEqual(record['sha256'], hashlib.sha256((output / record['file']).read_bytes()).hexdigest())
            self.assertEqual(record['rgba_sha256'], hashlib.sha256((output / (record['file'] + '.rgba')).read_bytes()).hexdigest())
        width, height = 19, 17
        expected = [(x * 31 + y * 59) % 256 for y in range(height) for x in range(width)]
        for mode in range(4):
            payload = (output / f'alpha-filter-{mode}.webp').read_bytes()
            self.assertEqual(struct.unpack_from('<I', payload, 4)[0] + 8, len(payload))
            position = payload.index(b'ALPH')
            size = struct.unpack_from('<I', payload, position + 4)[0]
            residuals = payload[position + 9:position + 8 + size]
            self.assertEqual(payload[position + 8], mode << 2)
            actual = []
            for index, value in enumerate(residuals):
                y, x = divmod(index, width)
                left = actual[index - 1] if x else 0
                above = actual[index - width] if y else 0
                corner = actual[index - width - 1] if x and y else 0
                prediction = 0 if mode == 0 else (left if y == 0 else (above if x == 0 or mode == 2 else (left if mode == 1 else max(0, min(255, left + above - corner)))))
                actual.append((value + prediction) & 255)
            self.assertEqual(actual, expected)


if __name__ == '__main__':
    unittest.main()
