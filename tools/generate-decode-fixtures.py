#!/usr/bin/env python3
"""Explicit offline developer oracle; never imported or invoked by the Go library.
Uses a user-selected libwebp shared library, ABI 0x0209, no fancy upsampling.
"""
import argparse, ctypes as C, hashlib, json, pathlib, struct
p=argparse.ArgumentParser();p.add_argument('--library',required=True);p.add_argument('--out',required=True);p.add_argument('--upstream',required=True);a=p.parse_args()
lib=C.CDLL(a.library);out=pathlib.Path(a.out);out.mkdir(parents=True,exist_ok=True)
class RGBA(C.Structure):_fields_=[('rgba',C.c_void_p),('stride',C.c_int),('size',C.c_size_t)]
class YUVA(C.Structure):_fields_=[('y',C.c_void_p),('u',C.c_void_p),('v',C.c_void_p),('a',C.c_void_p),('ys',C.c_int),('us',C.c_int),('vs',C.c_int),('as_',C.c_int),('yz',C.c_size_t),('uz',C.c_size_t),('vz',C.c_size_t),('az',C.c_size_t)]
class U(C.Union):_fields_=[('rgba',RGBA),('yuva',YUVA)]
class Buffer(C.Structure):_fields_=[('mode',C.c_int),('width',C.c_int),('height',C.c_int),('external',C.c_int),('u',U),('pad',C.c_uint32*4),('private',C.c_void_p)]
class Features(C.Structure):_fields_=[('w',C.c_int),('h',C.c_int),('alpha',C.c_int),('anim',C.c_int),('format',C.c_int),('pad',C.c_uint32*5)]
class Options(C.Structure):_fields_=[(s,C.c_int) for s in ['bypass','no_fancy','crop','left','top','width','height','scale','sw','sh','threads','dither','flip','alpha_dither']]+[('pad',C.c_uint32*5)]
class Config(C.Structure):_fields_=[('input',Features),('output',Buffer),('options',Options)]
lib.WebPInitDecoderConfigInternal.argtypes=[C.POINTER(Config),C.c_int];lib.WebPDecode.argtypes=[C.c_void_p,C.c_size_t,C.POINTER(Config)];lib.WebPFreeDecBuffer.argtypes=[C.POINTER(Buffer)]
lib.WebPEncodeRGBA.argtypes=[C.c_void_p,C.c_int,C.c_int,C.c_int,C.c_float,C.POINTER(C.c_void_p)];lib.WebPEncodeRGBA.restype=C.c_size_t;lib.WebPFree.argtypes=[C.c_void_p]
records=[]
def save(name,b,source):
 (out/name).write_bytes(b);c=Config();assert lib.WebPInitDecoderConfigInternal(C.byref(c),0x0209)==1;c.output.mode=1;c.options.no_fancy=1
 assert lib.WebPDecode(b,len(b),C.byref(c))==0,name
 pixels=b''.join(C.string_at(c.output.u.rgba.rgba+y*c.output.u.rgba.stride,c.output.width*4) for y in range(c.output.height))
 (out/(name+'.rgba')).write_bytes(pixels)
 records.append(dict(file=name,source=source,width=c.output.width,height=c.output.height,sha256=hashlib.sha256(b).hexdigest(),rgba_sha256=hashlib.sha256(pixels).hexdigest()))
 lib.WebPFreeDecBuffer(C.byref(c.output))
def encode(w,h,alpha=False):
 raw=bytes(c for y in range(h) for x in range(w) for c in ((x*47+y*13)%256,(x*17+y*71)%256,(x*97+y*3)%256,((x*31+y*59)%256 if alpha else 255)))
 ptr=C.c_void_p();n=lib.WebPEncodeRGBA(raw,w,h,w*4,83,C.byref(ptr));assert n;b=C.string_at(ptr,n);lib.WebPFree(ptr);return b
for name in ['blue-purple-pink.lossy.webp','blue-purple-pink-large.no-filter.lossy.webp','blue-purple-pink-large.normal-filter.lossy.webp','blue-purple-pink-large.simple-filter.lossy.webp']:
 save(name,(pathlib.Path(a.upstream)/name).read_bytes(),'golang.org/x/image v0.38.0/testdata/'+name)
for w,h in [(1,1),(1,17),(17,1),(19,17)]:save(f'pattern-{w}x{h}.webp',encode(w,h),'libwebp WebPEncodeRGBA quality 83; deterministic procedural pattern in this script')
def chunk(k,d):return k+struct.pack('<I',len(d))+d+(b'\0' if len(d)&1 else b'')
def riff(chunks):b=b'WEBP'+b''.join(chunks);return b'RIFF'+struct.pack('<I',len(b))+b
b=encode(19,17);vp8=b[20:20+struct.unpack('<I',b[16:20])[0]]
w,h=19,17;alpha=[(x*31+y*59)%256 for y in range(h) for x in range(w)]
for f in range(4):
 filtered=[]
 for y in range(h):
  for x in range(w):
   pred=0
   if f:
    if y==0:pred=alpha[y*w+x-1] if x else 0
    elif x==0:pred=alpha[(y-1)*w]
    elif f==1:pred=alpha[y*w+x-1]
    elif f==2:pred=alpha[(y-1)*w+x]
    else:pred=max(0,min(255,alpha[y*w+x-1]+alpha[(y-1)*w+x]-alpha[(y-1)*w+x-1]))
   filtered.append((alpha[y*w+x]-pred)%256)
 raw=riff([chunk(b'VP8X',bytes([16,0,0,0])+int(w-1).to_bytes(3,'little')+int(h-1).to_bytes(3,'little')),chunk(b'ALPH',bytes([f<<2])+bytes(filtered)),chunk(b'VP8 ',vp8)])
 save(f'alpha-filter-{f}.webp',raw,'Independent procedural ALPH residuals + libwebp color payload; oracle validates filters')
(out/'compressed-alpha.webp').write_bytes(encode(w,h,True))
(out/'provenance.json').write_text(json.dumps({'libwebp_version':hex(lib.WebPGetDecoderVersion()),'oracle_mode':'MODE_RGBA, no_fancy_upsampling=1, bypass_filtering=0, dithering=0, alpha_dithering=0','fixtures':records},indent=2)+'\n')
print(json.dumps({'version':hex(lib.WebPGetDecoderVersion()),'fixtures':len(records)}))
