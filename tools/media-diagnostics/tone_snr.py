"""Compare diagnostic raw f32le stereo/48k captures against a 1kHz tone.

Usage: python tone_snr.py FILE.f32 [FILE.f32 ...]
Requires numpy. Excludes the first/last two seconds; prints JSON only.
This is a comparative sine-fit measurement, not a perceptual quality score.
"""
import json
import sys
from pathlib import Path

import numpy as np


def measure(path):
    samples = np.fromfile(path, dtype="<f4")
    if len(samples) % 2:
        raise ValueError("expected interleaved stereo float32")
    audio = samples.reshape(-1, 2)[:, 0].astype(np.float64)
    rate, block = 48000, 4800
    t = np.arange(block) / rate
    basis = np.column_stack((np.sin(2 * np.pi * 1000 * t),
                             np.cos(2 * np.pi * 1000 * t)))
    values = []
    for start in range(2 * rate, len(audio) - 2 * rate - block + 1, block):
        window = audio[start:start + block]
        fit = basis @ np.linalg.lstsq(basis, window, rcond=None)[0]
        signal = np.mean(fit ** 2)
        noise = np.mean((window - fit) ** 2)
        if signal > 1e-10 and noise > 0:
            values.append(10 * np.log10(signal / noise))
    if not values:
        raise ValueError("no usable central tone windows")
    return {"file": str(path), "duration_s": len(audio) / rate,
            "windows": len(values), "snr_p50_db": float(np.median(values)),
            "snr_p05_db": float(np.percentile(values, 5))}


if __name__ == "__main__":
    if len(sys.argv) < 2:
        raise SystemExit(__doc__)
    print(json.dumps([measure(Path(p)) for p in sys.argv[1:]], indent=2))
