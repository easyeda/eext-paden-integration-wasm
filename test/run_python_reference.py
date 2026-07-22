import asyncio
import json
import math
import sys
import zipfile
from pathlib import Path

# Add the Python reference backend to sys.path
PYTHON_BACKEND = Path(__file__).resolve().parent.parent.parent / 'eext-paden-integration' / 'paden-service'
sys.path.insert(0, str(PYTHON_BACKEND))

import main

MIL_TO_MM = 0.0254


def mil_to_mm(v):
    return v * MIL_TO_MM


def layer_name(code):
    return 'TopLayer' if code == 'T' else 'BottomLayer' if code == 'B' else code


class FakeUploadFile:
    def __init__(self, filename, content):
        self.filename = filename
        self._content = content

    async def read(self):
        return self._content


def build_config(zip_path):
    with zipfile.ZipFile(zip_path, 'r') as zf:
        netlist = json.loads(zf.read('FlyingProbeTesting.json'))

    pins = netlist['pins']['rows']
    fields = netlist['pins']['fields']
    idx = {name: i for i, name in enumerate(fields)}

    seen = set()
    pads = []
    xs = []
    ys = []
    for row in pins:
        x = row[idx['PIN_X']]
        y = row[idx['PIN_Y']]
        net = row[idx['NET_NAME']]
        layer_code = row[idx['LAYER']]
        pin_type = row[idx['PIN_TYPE']]
        hole = row[idx['HOLE_SIZE']]
        pin_name = row[idx['PIN_NAME']]
        key = (round(x, 4), round(y, 4), net, layer_code)
        if key in seen:
            continue
        seen.add(key)
        is_tht = pin_type == 'DIP' or hole > 0
        pads.append({
            'x': mil_to_mm(x),
            'y': mil_to_mm(y),
            'layer': layer_name(layer_code),
            'net': net,
            'is_tht': is_tht,
            'hole_diameter': mil_to_mm(hole) if is_tht else 0.0,
            'pin_name': pin_name,
        })
        xs.append(x)
        ys.append(y)

    # Bounds in mm (slight margin not needed for center alignment)
    minx = mil_to_mm(min(xs))
    miny = mil_to_mm(min(ys))
    maxx = mil_to_mm(max(xs))
    maxy = mil_to_mm(max(ys))

    # Source: H1_2 / PAD4_2 (VCC input)
    # Load:  R1_1 / PAD5_1 (VCC load)
    source_pin_names = {'H1_2', 'PAD4_2'}
    load_pin_names = {'R1_1', 'PAD5_1'}
    source_vcc_pads = [p for p in pads if p['net'] == 'VCC' and p.get('pin_name', '') in source_pin_names]
    load_vcc_pads = [p for p in pads if p['net'] == 'VCC' and p.get('pin_name', '') in load_pin_names]
    gnd_pads = [p for p in pads if p['net'] == 'GND']

    if not source_vcc_pads:
        source_vcc_pads = [p for p in pads if p['net'] == 'VCC']
    if not load_vcc_pads:
        load_vcc_pads = [p for p in pads if p['net'] == 'VCC' and p not in source_vcc_pads]

    config = {
        'project_name': 'test-paden',
        'layers': [
            {'name': 'TopLayer', 'conductance': 5.95e4, 'layer_id': 1},
        ],
        'layer_cu_thickness': {
            'TopLayer': 0.035,
        },
        'vias': [],
        'pads': pads,
        'tracks': [],
        'rails': [],
        'gnd_net': 'GND',
        'temp_rise': 10.0,
        'easyeda_bounds': {
            'minX': minx,
            'minY': miny,
            'maxX': maxx,
            'maxY': maxy,
        },
        'sources': [
            {
                'net': 'VCC',
                'voltage': 5.0,
                'gnd_net': 'GND',
                'ref_des': 'H1',
                'pads': source_vcc_pads,
                'gnd_pads': gnd_pads,
            },
        ],
        'loads': [
            {
                'net': 'VCC',
                'current': 0.1,
                'gnd_net': 'GND',
                'ref_des': 'R1',
                'pads': load_vcc_pads,
                'gnd_pads': gnd_pads,
            },
        ],
    }
    return config


async def main_async():
    repo_root = Path(__file__).resolve().parent.parent
    zip_path = repo_root / 'test' / 'test-paden.zip'
    config = build_config(zip_path)
    config_json = json.dumps(config, ensure_ascii=False)

    config_path = repo_root / 'test' / 'config.json'
    with open(config_path, 'w', encoding='utf-8') as f:
        f.write(config_json)
    print(f'Config written to {config_path}')

    with open(zip_path, 'rb') as f:
        zip_bytes = f.read()

    result = await main.analyze_gerber(
        gerber=FakeUploadFile('test-paden.zip', zip_bytes),
        config=config_json,
    )

    out_path = repo_root / 'test' / 'python_result.json'
    # Pydantic v2
    data = result.model_dump() if hasattr(result, 'model_dump') else result.dict()
    with open(out_path, 'w', encoding='utf-8') as f:
        json.dump(data, f, ensure_ascii=False, indent=2)

    print(f'Result written to {out_path}')
    print(f'Success: {data.get("success")}')
    print(f'Message: {data.get("message")}')
    print(f'Diagnostics lines: {len(data.get("diagnostics", []))}')
    for line in data.get('diagnostics', [])[:10]:
        try:
            print(line)
        except UnicodeEncodeError:
            print(repr(line))


if __name__ == '__main__':
    asyncio.run(main_async())
