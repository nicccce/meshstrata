"""Extra read-only proofs appended to the unchanged T03 worker's dispatch."""
import http.client, struct
base_call = call

def call(v):
    if v['op'] == 'canary':
        with socket.create_connection(('127.0.0.1', 18080), timeout=3) as s:
            s.sendall(b'GET / HTTP/1.1\r\nHost: controlled.local\r\nConnection: close\r\n\r\n')
            raw = b''
            while b'meshstrata-i03c-controlled-loopback' not in raw:
                part = s.recv(4096)
                if not part: break
                raw += part
            assert b'meshstrata-i03c-controlled-loopback' in raw
        address, port = v['echo'].rsplit(':', 1)
        with socket.create_connection((address, int(port)), timeout=3) as s:
            payload = os.urandom(64); s.sendall(payload); raw = b''
            while len(raw) < len(payload):
                part = s.recv(len(payload)-len(raw)); assert part; raw += part
            assert raw == payload
        return dict(independent_http=True, independent_echo_bytes=64)
    if v['op'] == 'runtime-proof':
        folder = pathlib.Path(v['state']); assert folder.is_relative_to(ROOT)
        rows = []
        for p in folder.rglob('*'):
            if not p.is_file() or p.is_symlink() or p.stat().st_size > 8*1024*1024: continue
            if p.suffix not in ('.json', '.yaml', '.yml'): continue
            raw = p.read_bytes()
            rows.append(dict(path=str(p.relative_to(ROOT)), sha256=hashlib.sha256(raw).hexdigest(),
                             pin_ids={pin: pin.encode() in raw for pin in v['pins']}))
        active=[]
        for proc in pathlib.Path('/proc').iterdir():
            if not proc.name.isdigit(): continue
            try:
                if os.readlink(proc/'exe')!=str(ROOT/'mihomo'): continue
                argv=(proc/'cmdline').read_bytes().rstrip(b'\0').decode().split('\0')
                if '-f' not in argv: continue
                config=pathlib.Path(argv[argv.index('-f')+1])
                if not config.is_relative_to(folder): continue
                raw=config.read_bytes(); value=json.loads(raw)
                sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);sock.settimeout(3)
                sock.connect(str(folder/'runtime/api/m.sock'))
                peer_pid=struct.unpack('3i',sock.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))[0]
                assert peer_pid==int(proc.name)
                connection=http.client.HTTPConnection('mihomo',timeout=3);connection.sock=sock
                connection.request('GET','/proxies',headers={'Authorization':'Bearer '+value['secret']})
                response=connection.getresponse();body=response.read(1024*1024+1);assert response.status==200 and len(body)<=1024*1024
                live=json.loads(body);connection.close()
                active.append(dict(api_peer_pid=peer_pid,live_egress_names=sorted(name for name in live['proxies'] if name.startswith('egress_')),pid=int(proc.name), start=(proc/'stat').read_text().split()[21],
                                   path=str(config.relative_to(ROOT)),sha256=hashlib.sha256(raw).hexdigest(),
                                   egress_names=[p['name'] for p in value.get('proxies',[]) if p.get('name','').startswith('egress_')],
                                   listener_names=[p['name'] for p in value.get('listeners',[])]))
            except (OSError,ValueError,KeyError): continue
        return dict(files=rows,active=active)
    return base_call(v)
