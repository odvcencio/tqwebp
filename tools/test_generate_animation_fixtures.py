"""Offline animation-producer contracts; fake C buffers are not pixel-oracle evidence.

These tests call the actual producer functions. Owned output buffers and borrowed
canvases are real ctypes allocations, but no shared library is loaded and no fake
pixel data is presented as independent codec or compositing evidence.
"""
import contextlib
import ctypes
import importlib.util
import io
from pathlib import Path
import struct
import unittest
from unittest import mock


COPY = ctypes.string_at


def load_producer():
    spec = importlib.util.spec_from_file_location(
        'animation_fixture_generator', Path(__file__).with_name('generate-animation-fixtures.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


# Guard the initial import too, rather than only testing a second import.
with mock.patch.object(ctypes, 'CDLL', side_effect=AssertionError('native load during import')):
    generator = load_producer()


def riff(body):
    """Independent framing, including malformed bodies used by negative tests."""
    return b'RIFF' + struct.pack('<I', len(body) + 4) + b'WEBP' + body


def chunk(tag, payload):
    return tag + struct.pack('<I', len(payload)) + payload + b'\0' * (len(payload) & 1)


def address(pointer):
    return pointer.value if isinstance(pointer, ctypes.c_void_p) else pointer


class Function:
    def __init__(self, callback):
        self.callback = callback

    def __call__(self, *args):
        return self.callback(*args)


class Library:
    def __init__(self, events):
        self.events = events
        self.buffers = []
        self.frees = []
        self.inputs = []
        self.payload = riff(chunk(b'VP8 ', b'neutral output'))
        self.encode_status = None
        self.encode_pointer = True
        self.encode_exception = None
        self.WebPEncodeRGBA = Function(self.encode)
        self.WebPFree = Function(self.free)

    def allocate(self, payload):
        buffer = ctypes.create_string_buffer(payload)
        self.buffers.append(buffer)
        return ctypes.addressof(buffer)

    def free(self, pointer):
        pointer = address(pointer)
        self.frees.append(pointer)
        self.events.append(('free', pointer))

    def encode(self, raw, width, height, stride, quality, output):
        self.inputs.append((raw, width, height, stride, quality))
        if self.encode_pointer:
            output._obj.value = self.allocate(self.payload)
        if self.encode_exception is not None:
            raise self.encode_exception
        return len(self.payload) if self.encode_status is None else self.encode_status


class Mux:
    def __init__(self, lib, events):
        self.lib = lib
        self.events = events
        self.handle = 0x123456789
        self.fail = None
        self.fail_status = -1
        self.failure = None
        self.calls = []
        self.deletes = []
        self.canvas = None
        self.params = None
        self.frames = []
        self.metadata = []
        self.output = riff(chunk(b'TEST', b'assembled'))
        self.output_pointer = True
        self.output_size = None
        self.output_address = None
        self.WebPNewInternal = Function(self.create)
        self.WebPMuxDelete = Function(self.delete)
        self.WebPMuxSetCanvasSize = Function(self.set_canvas)
        self.WebPMuxSetAnimationParams = Function(self.set_params)
        self.WebPMuxPushFrame = Function(self.push_frame)
        self.WebPMuxSetChunk = Function(self.set_chunk)
        self.WebPMuxAssemble = Function(self.assemble)

    def stage(self, stage, handle):
        if handle != self.handle:
            raise AssertionError('mux handle changed')
        self.calls.append(stage)
        if stage == self.fail:
            if self.failure is not None:
                raise self.failure
            return self.fail_status
        return 1

    def create(self, abi):
        self.calls.append(('create', abi))
        return self.handle

    def delete(self, handle):
        self.deletes.append(handle)
        self.events.append(('mux delete', handle))

    def set_canvas(self, handle, width, height):
        self.canvas = (width, height)
        return self.stage('canvas', handle)

    def set_params(self, handle, ref):
        self.params = (ref._obj.background, ref._obj.loops)
        return self.stage('animation parameters', handle)

    def push_frame(self, handle, ref, copy_data):
        frame = ref._obj
        self.frames.append((COPY(frame.bitstream.bytes, frame.bitstream.size),
                            frame.x, frame.y, frame.duration, frame.id,
                            frame.dispose, frame.blend, copy_data))
        return self.stage('push frame ' + str(len(self.frames)), handle)

    def set_chunk(self, handle, tag, ref, copy_data):
        data = ref._obj
        self.metadata.append((tag, COPY(data.bytes, data.size), copy_data))
        return self.stage('metadata ' + tag.decode('ascii'), handle)

    def assemble(self, handle, ref):
        if self.output_pointer:
            self.output_address = self.lib.allocate(self.output)
            ref._obj.bytes = self.output_address
        ref._obj.size = len(self.output) if self.output_size is None else self.output_size
        return self.stage('assemble', handle)


class Demux:
    def __init__(self, events):
        self.events = events
        self.handle = 0x23456789a
        self.init_status = self.info_status = 1
        self.width, self.height = 2, 1
        self.loops, self.background = 7, 0x7f123456
        self.payloads = [b'\x13\x39\x5b\0\x01\x02\x03\xff', bytes(range(8))]
        self.timestamps = [0, 33]
        self.count = len(self.payloads)
        self.more_override = None
        self.next_status = 1
        self.null_next = False
        self.next_exception = None
        self.index = 0
        self.calls = []
        self.deletes = []
        self.input = None
        self.options = None
        self.buffer = None
        self.WebPAnimDecoderOptionsInitInternal = Function(self.initialize)
        self.WebPAnimDecoderNewInternal = Function(self.create)
        self.WebPAnimDecoderGetInfo = Function(self.info)
        self.WebPAnimDecoderHasMoreFrames = Function(self.has_more)
        self.WebPAnimDecoderGetNext = Function(self.next)
        self.WebPAnimDecoderDelete = Function(self.delete)

    def initialize(self, ref, abi):
        self.calls.append(('initialize', abi))
        # Non-default initial values ensure producer explicitly selects its policy.
        ref._obj.color, ref._obj.threads = 3, 1
        return self.init_status

    def create(self, data_ref, options_ref, abi):
        data, options = data_ref._obj, options_ref._obj
        self.calls.append(('create', abi))
        self.input = COPY(data.bytes, data.size)
        self.options = (options.color, options.threads)
        return self.handle

    def called(self, name, handle):
        if handle != self.handle:
            raise AssertionError('decoder handle changed')
        self.calls.append(name)

    def info(self, handle, ref):
        self.called('info', handle)
        info = ref._obj
        info.width, info.height, info.frames = self.width, self.height, self.count
        info.loops, info.background = self.loops, self.background
        return self.info_status

    def has_more(self, handle):
        self.called('has more', handle)
        if self.more_override is not None:
            return self.more_override.pop(0)
        return self.index < len(self.payloads)

    def next(self, handle, pixels, timestamp):
        self.called('next', handle)
        payload = self.payloads[self.index]
        # Native anim decoders reuse borrowed storage. Reuse the allocation to
        # catch a producer that returns pointers instead of copied Python bytes.
        if self.buffer is None:
            self.buffer = ctypes.create_string_buffer(max(map(len, self.payloads)))
        ctypes.memmove(self.buffer, payload, len(payload))
        if not self.null_next:
            pixels._obj.value = ctypes.addressof(self.buffer)
        timestamp._obj.value = self.timestamps[self.index]
        self.index += 1
        if self.next_exception is not None:
            raise self.next_exception
        return self.next_status

    def delete(self, handle):
        self.deletes.append(handle)
        self.events.append(('decoder delete', handle))


class AnimationFixtureContractTests(unittest.TestCase):
    def setUp(self):
        guard = mock.patch.object(ctypes, 'CDLL', side_effect=AssertionError('tests must remain offline'))
        self.load = guard.start()
        self.addCleanup(guard.stop)
        self.events = []
        self.lib = Library(self.events)
        self.mux = Mux(self.lib, self.events)
        self.demux = Demux(self.events)
        generator.bind(self.mux, self.demux)
        self.frames = [
            dict(encoded=b'first\0encoded', x=2, y=0, duration=0, dispose=True, replace=False),
            dict(encoded=b'second', x=0, y=2, duration=16777215, dispose=False, replace=True),
        ]

    def assemble(self):
        return generator.assemble(self.lib, self.mux, 7, 5, 65535, 0x7f123456, self.frames)

    def oracle(self):
        return generator.oracle(self.demux, b'encoded\0animation')

    def assert_mux_release(self, output=False):
        self.assertEqual(self.mux.deletes, [self.mux.handle])
        expected = [('mux delete', self.mux.handle)]
        if output:
            self.assertEqual(self.lib.frees, [self.mux.output_address])
            expected.insert(0, ('free', self.mux.output_address))
        else:
            self.assertEqual(self.lib.frees, [])
        self.assertEqual(self.events, expected)

    def assert_decoder_release(self):
        self.assertEqual(self.demux.deletes, [self.demux.handle])
        self.assertEqual(self.events, [('decoder delete', self.demux.handle)])
        self.assertEqual(self.lib.frees, [])  # canvas storage belongs to decoder

    def test_import_does_not_load_libraries_or_generate_files(self):
        with mock.patch.object(Path, 'mkdir') as mkdir, \
                mock.patch.object(Path, 'write_bytes') as write_bytes, \
                mock.patch.object(Path, 'write_text') as write_text:
            load_producer()
        self.load.assert_not_called()
        mkdir.assert_not_called()
        write_bytes.assert_not_called()
        write_text.assert_not_called()

    def test_each_library_and_output_argument_is_required_before_loading(self):
        values = {'library': '/chosen/libwebp.so', 'mux-library': '/chosen/libwebpmux.so',
                  'demux-library': '/chosen/libwebpdemux.so', 'out': '/not-created'}
        for missing in values:
            with self.subTest(missing=missing):
                args = [value for key, path in values.items() if key != missing
                        for value in ('--' + key, path)]
                with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                    generator.main(args)
                self.assertEqual(error.exception.code, 2)
        self.load.assert_not_called()

    def test_binding_preserves_pointer_width_and_output_pointer_types(self):
        self.assertIs(self.mux.WebPNewInternal.restype, ctypes.c_void_p)
        self.assertIs(self.demux.WebPAnimDecoderNewInternal.restype, ctypes.c_void_p)
        self.assertEqual(self.mux.WebPMuxAssemble.argtypes,
                         [ctypes.c_void_p, ctypes.POINTER(generator.Data)])
        self.assertEqual(self.demux.WebPAnimDecoderGetNext.argtypes,
                         [ctypes.c_void_p, ctypes.POINTER(ctypes.c_void_p), ctypes.POINTER(ctypes.c_int)])

    def test_mux_creation_failure_owns_no_handle_or_output(self):
        self.mux.handle = None
        with self.assertRaisesRegex(RuntimeError, 'mux creation failed'):
            self.assemble()
        self.assertEqual(self.mux.calls, [('create', 0x0109)])
        self.assertEqual(self.events, [])

    def test_mux_success_preserves_controls_metadata_and_copied_bytes(self):
        result = self.assemble()
        self.assertEqual(result, self.mux.output)
        self.assertIsInstance(result, bytes)
        self.assertEqual(self.mux.canvas, (7, 5))
        self.assertEqual(self.mux.params, (0x7f123456, 65535))
        self.assertEqual(self.mux.frames, [(b'first\0encoded', 2, 0, 0, 3, 1, 0, 1),
                                         (b'second', 0, 2, 16777215, 3, 0, 1, 1)])
        self.assertEqual(self.mux.metadata, [(b'ICCP', b'profile', 1),
                                           (b'EXIF', b'late-exif', 1), (b'XMP ', b'xmp', 1)])
        self.assertEqual(self.mux.calls[0], ('create', 0x0109))
        self.assert_mux_release(output=True)
        ctypes.memset(self.mux.output_address, 0, len(result))
        self.assertEqual(result, self.mux.output)

    def test_every_mux_setup_failure_deletes_handle_and_stops(self):
        stages = ['canvas', 'animation parameters', 'push frame 1', 'push frame 2',
                  'metadata ICCP', 'metadata EXIF', 'metadata XMP ']
        for stage in stages:
            with self.subTest(stage=stage):
                self.events.clear()
                self.mux = Mux(self.lib, self.events)
                self.mux.fail = stage
                with self.assertRaisesRegex(RuntimeError, 'failed: -1'):
                    self.assemble()
                self.assertEqual(self.mux.calls, [('create', 0x0109)] + stages[:stages.index(stage) + 1])
                self.assert_mux_release()

    def test_mux_setup_exception_is_preserved_after_delete(self):
        failure = OSError('push call failed')
        self.mux.fail, self.mux.failure = 'push frame 2', failure
        with self.assertRaises(OSError) as error:
            self.assemble()
        self.assertIs(error.exception, failure)
        self.assert_mux_release()

    def test_mux_assemble_failure_frees_nonnull_output_before_delete(self):
        self.mux.fail = 'assemble'
        with self.assertRaisesRegex(RuntimeError, 'assemble failed: -1'):
            self.assemble()
        self.assert_mux_release(output=True)

    def test_mux_assemble_exception_frees_nonnull_output_before_delete(self):
        failure = OSError('assemble call failed after allocation')
        self.mux.fail, self.mux.failure = 'assemble', failure
        with self.assertRaises(OSError) as error:
            self.assemble()
        self.assertIs(error.exception, failure)
        self.assert_mux_release(output=True)

    def test_mux_assemble_failure_with_null_output_does_not_free(self):
        self.mux.fail, self.mux.output_pointer = 'assemble', False
        with self.assertRaisesRegex(RuntimeError, 'assemble failed: -1'):
            self.assemble()
        self.assert_mux_release()

    def test_mux_success_with_zero_size_frees_nonnull_output(self):
        self.mux.output_size = 0
        with self.assertRaisesRegex(RuntimeError, 'empty assembled output'):
            self.assemble()
        self.assert_mux_release(output=True)

    def test_mux_success_with_null_output_does_not_copy_or_free(self):
        self.mux.output_pointer = False
        with mock.patch.object(generator.C, 'string_at') as copy:
            with self.assertRaisesRegex(RuntimeError, 'empty assembled output'):
                self.assemble()
        copy.assert_not_called()
        self.assert_mux_release()

    def test_mux_output_copy_exception_frees_output_and_handle(self):
        failure = ValueError('assembled output copy failed')
        with mock.patch.object(generator.C, 'string_at', side_effect=failure):
            with self.assertRaises(ValueError) as error:
                self.assemble()
        self.assertIs(error.exception, failure)
        self.assert_mux_release(output=True)

    def test_decoder_abi_failure_does_not_create_or_delete(self):
        self.demux.init_status = 0
        with self.assertRaisesRegex(RuntimeError, 'decoder ABI failed: 0'):
            self.oracle()
        self.assertEqual(self.demux.calls, [('initialize', 0x0107)])
        self.assertEqual(self.events, [])

    def test_decoder_creation_failure_owns_no_handle(self):
        self.demux.handle = None
        with self.assertRaisesRegex(RuntimeError, 'animation decoder creation failed'):
            self.oracle()
        self.assertEqual(self.demux.calls, [('initialize', 0x0107), ('create', 0x0107)])
        self.assertEqual(self.events, [])

    def test_decoder_success_copies_borrowed_canvases_and_preserves_policy(self):
        info, canvases = self.oracle()
        self.assertEqual(self.demux.input, b'encoded\0animation')
        self.assertEqual(self.demux.options, (1, 0))
        self.assertEqual((info.width, info.height, info.frames, info.loops, info.background),
                         (2, 1, 2, 7, 0x7f123456))
        self.assertEqual(canvases, list(zip(self.demux.payloads, self.demux.timestamps)))
        self.assertEqual(self.demux.calls, [('initialize', 0x0107), ('create', 0x0107),
                         'info', 'has more', 'next', 'has more', 'next', 'has more'])
        self.assert_decoder_release()
        ctypes.memset(self.demux.buffer, 0, 8)
        self.assertEqual(canvases[0][0], self.demux.payloads[0])
        self.assertEqual(canvases[1][0], self.demux.payloads[1])

    def test_decoder_info_failure_deletes_before_returning_error(self):
        self.demux.info_status = 0
        with self.assertRaisesRegex(RuntimeError, 'animation info failed: 0'):
            self.oracle()
        self.assertNotIn('has more', self.demux.calls)
        self.assert_decoder_release()

    def test_decoder_rejects_invalid_dimensions_and_frame_counts_before_copy(self):
        cases = [(0, 1, 1), (1, 0, 1), (1001, 1000, 1), (0xffffffff, 0xffffffff, 1),
                 (1, 1, 0), (1, 1, 1001), (1, 1, 0xffffffff)]
        for width, height, count in cases:
            with self.subTest(width=width, height=height, count=count):
                self.events.clear()
                self.demux = Demux(self.events)
                self.demux.width, self.demux.height, self.demux.count = width, height, count
                with mock.patch.object(generator.C, 'string_at') as copy:
                    with self.assertRaisesRegex(RuntimeError, 'dimensions/count outside fixture policy'):
                        self.oracle()
                copy.assert_not_called()
                self.assertNotIn('has more', self.demux.calls)
                self.assert_decoder_release()

    def test_decoder_accepts_inclusive_canvas_policy_boundary(self):
        self.demux.width = self.demux.height = 1000
        self.demux.count = 1
        self.demux.payloads = [b'\x13\x39\x5b\0' * 1_000_000]
        info, canvases = self.oracle()
        self.assertEqual(info.width * info.height, 1_000_000)
        self.assertEqual(canvases, [(self.demux.payloads[0], 0)])
        self.assert_decoder_release()

    def test_decoder_accepts_inclusive_frame_count_boundary(self):
        self.demux.width = self.demux.height = 1
        self.demux.count = 1000
        self.demux.payloads = [b'\x13\x39\x5b\0'] * 1000
        self.demux.timestamps = list(range(1000))
        info, canvases = self.oracle()
        self.assertEqual(info.frames, 1000)
        self.assertEqual(canvases, list(zip(self.demux.payloads, self.demux.timestamps)))
        self.assert_decoder_release()

    def test_decoder_next_failure_does_not_copy_borrowed_pixels(self):
        self.demux.next_status = 0
        with mock.patch.object(generator.C, 'string_at') as copy:
            with self.assertRaisesRegex(RuntimeError, 'next animation frame failed: 0'):
                self.oracle()
        copy.assert_not_called()
        self.assertEqual(self.demux.index, 1)
        self.assert_decoder_release()

    def test_decoder_null_pixels_rejected_without_copy(self):
        self.demux.null_next = True
        with mock.patch.object(generator.C, 'string_at') as copy:
            with self.assertRaisesRegex(RuntimeError, 'missing canvas pixels'):
                self.oracle()
        copy.assert_not_called()
        self.assert_decoder_release()

    def test_decoder_early_end_before_any_or_after_one_frame_deletes(self):
        for responses, expected_next in [([False], 0), ([True, False], 1)]:
            with self.subTest(responses=responses):
                self.events.clear()
                self.demux = Demux(self.events)
                self.demux.more_override = list(responses)
                with self.assertRaisesRegex(RuntimeError, 'early animation end'):
                    self.oracle()
                self.assertEqual(self.demux.index, expected_next)
                self.assert_decoder_release()

    def test_decoder_extra_frame_is_rejected_without_reading_it(self):
        self.demux.count = 1
        with self.assertRaisesRegex(RuntimeError, 'unexpected extra animation frame'):
            self.oracle()
        self.assertEqual(self.demux.index, 1)
        self.assert_decoder_release()

    def test_decoder_copy_failure_on_each_frame_deletes(self):
        for fail_at in (1, 2):
            with self.subTest(frame=fail_at):
                self.events.clear()
                self.demux = Demux(self.events)
                failure = ValueError('canvas copy failed')
                copies = []
                def copy(pointer, size):
                    copies.append((pointer.value, size))
                    if len(copies) == fail_at:
                        raise failure
                    return COPY(pointer, size)
                with mock.patch.object(generator.C, 'string_at', side_effect=copy):
                    with self.assertRaises(ValueError) as error:
                        self.oracle()
                self.assertIs(error.exception, failure)
                self.assertEqual(self.demux.index, fail_at)
                self.assertEqual([size for _, size in copies], [8] * fail_at)
                self.assert_decoder_release()

    def test_decoder_call_exceptions_preserve_error_and_delete(self):
        for name in ['WebPAnimDecoderGetInfo', 'WebPAnimDecoderHasMoreFrames',
                     'WebPAnimDecoderGetNext']:
            with self.subTest(call=name):
                self.events.clear()
                self.demux = Demux(self.events)
                failure = OSError('decoder call failed')
                if name == 'WebPAnimDecoderGetNext':
                    self.demux.next_exception = failure  # fail with borrowed pixels set
                else:
                    getattr(self.demux, name).callback = mock.Mock(side_effect=failure)
                with self.assertRaises(OSError) as error:
                    self.oracle()
                self.assertIs(error.exception, failure)
                self.assert_decoder_release()

    def test_neutral_encoder_success_inputs_and_release(self):
        result = generator.neutral_lossy(self.lib, 3, 2, 30, 97)
        self.assertEqual(result, self.lib.payload)
        self.assertEqual(self.lib.inputs, [(bytes([30, 30, 30, 97]) * 6, 3, 2, 12, 90)])
        self.assertEqual(self.lib.frees, [ctypes.addressof(self.lib.buffers[0])])
        ctypes.memset(self.lib.buffers[0], 0, len(result))
        self.assertEqual(result, self.lib.payload)

    def test_neutral_encoder_default_alpha_is_opaque(self):
        generator.neutral_lossy(self.lib, 1, 1, 140)
        self.assertEqual(self.lib.inputs, [(bytes([140, 140, 140, 255]), 1, 1, 4, 90)])
        self.assertEqual(len(self.lib.frees), 1)

    def test_neutral_encoder_failure_frees_nonnull_output(self):
        self.lib.encode_status = 0
        with self.assertRaisesRegex(RuntimeError, 'neutral encode failed'):
            generator.neutral_lossy(self.lib, 1, 1, 30)
        self.assertEqual(self.lib.frees, [ctypes.addressof(self.lib.buffers[0])])

    def test_neutral_encoder_null_output_does_not_copy_or_free(self):
        for status in (0, 7):
            with self.subTest(status=status):
                self.lib.encode_pointer, self.lib.encode_status = False, status
                with mock.patch.object(generator.C, 'string_at') as copy:
                    with self.assertRaisesRegex(RuntimeError, 'neutral encode failed'):
                        generator.neutral_lossy(self.lib, 1, 1, 30)
                copy.assert_not_called()
        self.assertEqual(self.lib.frees, [])

    def test_neutral_encoder_call_exception_frees_nonnull_output(self):
        failure = OSError('encoder call failed after allocation')
        self.lib.encode_exception = failure
        with self.assertRaises(OSError) as error:
            generator.neutral_lossy(self.lib, 1, 1, 30)
        self.assertIs(error.exception, failure)
        self.assertEqual(self.lib.frees, [ctypes.addressof(self.lib.buffers[0])])

    def test_neutral_encoder_copy_exception_frees_nonnull_output(self):
        failure = ValueError('encoded copy failed')
        with mock.patch.object(generator.C, 'string_at', side_effect=failure):
            with self.assertRaises(ValueError) as error:
                generator.neutral_lossy(self.lib, 1, 1, 30)
        self.assertIs(error.exception, failure)
        self.assertEqual(self.lib.frees, [ctypes.addressof(self.lib.buffers[0])])

    def test_riff_controls_preserve_order_duplicates_unknowns_and_odd_padding(self):
        entries = [(b'VP8X', b'12'), (b'U001', b'odd'), (b'ANMF', b''),
                   (b'EXIF', b'early'), (b'EXIF', b'late'), (b'XMP ', b'last')]
        self.assertEqual(generator.chunks(riff(b''.join(chunk(*item) for item in entries))), entries)
        self.assertEqual(generator.chunks(riff(b'')), [])

    def test_riff_rejects_short_headers_magic_and_declared_extent_mismatches(self):
        valid = riff(chunk(b'TEST', b'ab'))
        invalid = [valid[:size] for size in range(12)]
        invalid += [b'RIFX' + valid[4:], valid[:8] + b'WAVE' + valid[12:],
                    valid + b'\0', valid[:-1],
                    valid[:4] + struct.pack('<I', 0) + valid[8:],
                    valid[:4] + struct.pack('<I', 0xffffffff) + valid[8:]]
        for data in invalid:
            with self.subTest(data=data):
                with self.assertRaisesRegex(ValueError, 'invalid RIFF'):
                    generator.chunks(data)

    def test_riff_rejects_every_short_chunk_header_including_trailing_fragments(self):
        for length in range(1, 8):
            for prefix in (b'', chunk(b'GOOD', b'odd')):
                with self.subTest(length=length, prefix=bool(prefix)):
                    with self.assertRaisesRegex(ValueError, 'short chunk'):
                        generator.chunks(riff(prefix + b'X' * length))

    def test_riff_rejects_payload_overrun_and_missing_odd_padding(self):
        malformed = [b'TEST' + struct.pack('<I', 4) + b'abc',
                     b'TEST' + struct.pack('<I', 1) + b'x',
                     b'TEST' + struct.pack('<I', 0xffffffff),
                     chunk(b'GOOD', b'ok') + b'LAST' + struct.pack('<I', 3) + b'ab']
        for body in malformed:
            with self.subTest(body=body):
                with self.assertRaisesRegex(ValueError, 'chunk exceeds RIFF'):
                    generator.chunks(riff(body))

    def test_constant_lossless_header_and_literal_tree_controls(self):
        for width, height, rgba in [(3, 5, [19, 57, 91, 0]), (1, 1, [1, 2, 3, 255]),
                                    (16384, 1, [0, 255, 128, 129])]:
            with self.subTest(width=width, height=height, rgba=rgba):
                data = generator.constant_lossless(width, height, rgba)
                self.assertEqual(data[:4], b'RIFF')
                self.assertEqual(struct.unpack_from('<I', data, 4)[0] + 8, len(data))
                self.assertEqual(data[8:16], b'WEBPVP8L')
                size = struct.unpack_from('<I', data, 16)[0]
                payload = data[20:20 + size]
                self.assertEqual(payload[:5], b'\x2f' + struct.pack('<I',
                                 (width - 1) | ((height - 1) << 14) | ((rgba[3] != 255) << 28)))
                bits = [(value >> bit) & 1 for value in payload[5:] for bit in range(8)]
                self.assertEqual(bits[:3], [0, 0, 0])
                offset = 3
                for value in (rgba[1], rgba[0], rgba[2], rgba[3], 0):
                    self.assertEqual(bits[offset:offset + 3], [1, 0, 1])
                    self.assertEqual(sum(bit << i for i, bit in enumerate(bits[offset + 3:offset + 11])), value)
                    offset += 11
                self.assertEqual(bits[offset:], [0] * 6)
                self.assertEqual(data[20 + size:], b'\0')

    def test_frame_and_case_controls_keep_adversarial_inputs_and_neutral_encodes(self):
        item = generator.frame(3, 1, 2, 0, [19, 57, 91, 0], 0, replace=True, dispose=True)
        self.assertEqual({k: v for k, v in item.items() if k != 'encoded'},
                         dict(width=3, height=1, x=2, y=0, rgba=[19, 57, 91, 0],
                              duration=0, replace=True, dispose=True))
        self.assertEqual(item['encoded'], generator.constant_lossless(3, 1, [19, 57, 91, 0]))
        cases = generator.cases(self.lib)
        by_name = {case[0]: case for case in cases}
        self.assertEqual(set(by_name), {'blend-dispose', 'replacement-transparency',
                         'key-after-dispose', 'one-frame-animation', 'mixed-neutral',
                         'max-duration', 'adversarial-alpha-sequence'})
        self.assertEqual([args[0][0:4] for args in self.lib.inputs],
                         [bytes([140, 140, 140, 255]), bytes([30, 30, 30, 97])])
        self.assertEqual(len(self.lib.frees), 2)
        maximum = by_name['max-duration']
        self.assertEqual(maximum[3], 65535)
        self.assertEqual([frame['duration'] for frame in maximum[4]], [16777215, 0])
        adversarial = by_name['adversarial-alpha-sequence'][4]
        self.assertEqual(len(adversarial), 64)
        self.assertEqual({frame['rgba'][3] for frame in adversarial},
                         {0, 255, 1, 254, 2, 253, 3, 128, 127, 129, 64, 192})
        self.assertEqual({frame['duration'] for frame in adversarial}, {0, 1, 17, 33})
        for frame in adversarial:
            self.assertEqual((frame['x'] % 2, frame['y'] % 2), (0, 0))
            self.assertLessEqual(frame['x'] + frame['width'], 7)
            self.assertLessEqual(frame['y'] + frame['height'], 5)


if __name__ == '__main__':
    unittest.main()
