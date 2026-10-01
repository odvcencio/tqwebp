import ctypes
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name('verify-encode-fixtures.py')

def load():
    spec = importlib.util.spec_from_file_location('encode_verifier', SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

class VerifierTests(unittest.TestCase):
    def setUp(self):
        self.module = load()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name)
        self.case = dict(name='sample', width=1, height=1, animated=True, loops=3,
                         frames=[dict(source='source.rgba', duration_ms=17)])
        self.pixels = bytes([40,40,40,127])
        (self.path/'sample.webp').write_bytes(b'encoded')
        (self.path/'source.rgba').write_bytes(self.pixels)
        self.lib = mock.Mock()
        self.lib.WebPGetDecoderVersion.return_value = 0x10500
        self.info = self.module.animation.Info(1,1,3,0,1)

    def run_verify(self, frames=None):
        (self.path/'inputs.json').write_text(json.dumps([self.case]))
        if frames is None:
            frames = [(self.pixels,17)]
        with mock.patch.object(self.module.animation, 'oracle', return_value=(self.info,frames)):
            return self.module.verify(self.lib, mock.Mock(), self.path)

    def test_import_is_offline(self):
        with mock.patch.object(ctypes,'CDLL',side_effect=AssertionError('native load')):
            load()

    def test_animation_success(self):
        result = self.run_verify()
        self.assertEqual(result['libwebp'],'0x10500')
        self.assertEqual((self.path/'sample-00.oracle.rgba').read_bytes(),self.pixels)
        self.assertEqual(result['frames'][0]['timestamp_ms'],17)

    def test_controls_rejected(self):
        for attr in ('width','height','frames','loops'):
            with self.subTest(attr=attr):
                old=getattr(self.info,attr);setattr(self.info,attr,old+1)
                with self.assertRaisesRegex(RuntimeError,'control mismatch'):
                    self.run_verify()
                setattr(self.info,attr,old)

    def test_frame_count_rejected(self):
        with self.assertRaisesRegex(RuntimeError,'frame count mismatch'):
            self.run_verify([])

    def test_alpha_rejected(self):
        with self.assertRaisesRegex(RuntimeError,'alpha mismatch'):
            self.run_verify([(bytes([40,40,40,128]),17)])

    def test_pixel_length_rejected(self):
        with self.assertRaisesRegex(RuntimeError,'alpha mismatch'):
            self.run_verify([(b'',17)])

    def test_timestamp_rejected(self):
        with self.assertRaisesRegex(RuntimeError,'timestamp mismatch'):
            self.run_verify([(self.pixels,18)])

    def test_oracle_failure_preserved(self):
        (self.path/'inputs.json').write_text(json.dumps([self.case]))
        sentinel=RuntimeError('native oracle failed')
        with mock.patch.object(self.module.animation,'oracle',side_effect=sentinel):
            with self.assertRaises(RuntimeError) as caught:
                self.module.verify(self.lib,mock.Mock(),self.path)
        self.assertIs(caught.exception,sentinel)
        self.assertFalse((self.path/'oracle.json').exists())

    def still(self, width=1, height=1):
        self.case['animated']=False;self.case['loops']=0;self.case['frames'][0]['duration_ms']=0
        def save(lib,out,records,name,payload,source):
            self.assertEqual(payload,b'encoded')
            records.append(dict(width=width,height=height))
            (out/(name+'.rgba')).write_bytes(self.pixels)
        return mock.patch.object(self.module.animation.helper,'save',side_effect=save)

    def test_still_helper_used(self):
        with self.still():
            result=self.run_verify()
        self.assertEqual(result['frames'][0]['timestamp_ms'],0)

    def test_still_dimensions_rejected(self):
        with self.still(width=2):
            with self.assertRaisesRegex(RuntimeError,'dimensions mismatch'):
                self.run_verify()

    def test_write_failure_preserved(self):
        real=Path.write_bytes
        sentinel=OSError('destination failed')
        def write(path,data):
            if path.name.endswith('.oracle.rgba'):
                raise sentinel
            return real(path,data)
        with mock.patch.object(Path,'write_bytes',write):
            with self.assertRaises(OSError) as caught:
                self.run_verify()
        self.assertIs(caught.exception,sentinel)
        self.assertFalse((self.path/'oracle.json').exists())

    def test_explicit_paths_required(self):
        with mock.patch.object(ctypes,'CDLL',side_effect=AssertionError('load before args')):
            with mock.patch('sys.stderr'):
                with self.assertRaises(SystemExit) as caught:
                    self.module.main([])
        self.assertEqual(caught.exception.code,2)

if __name__=='__main__':
    unittest.main()
