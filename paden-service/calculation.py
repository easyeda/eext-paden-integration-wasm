#!/usr/bin/env python3
"""
PCB 走线宽度计算器 — 基于 IPC-2221 标准
用法：直接跑脚本看 demo，或 import 调用函数
"""

import math
from dataclasses import dataclass
from typing import Literal

# ── 常量 ──────────────────────────────────────────────
MIL_PER_OZ = 1.378          # 1 oz 铜厚 ≈ 1.378 mil
MIL_TO_MM  = 0.0254         # 1 mil = 0.0254 mm
OZ_TO_MM   = MIL_PER_OZ * MIL_TO_MM   # 1 oz ≈ 0.03498 mm ≈ 35 μm

LAYER_K = {
    "outer": 0.048,
    "inner": 0.024,
}
# 指数
B = 0.44
C = 0.725


@dataclass
class TraceResult:
    area_mil2:   float   # 所需截面积 (mil²)
    width_mil:   float   # 线宽 (mil)
    width_mm:    float   # 线宽 (mm)
    thickness_mil: float # 铜厚 (mil)
    thickness_um: float  # 铜厚 (μm)
    current:     float   # 输入电流
    temp_rise:   float   # 温升
    layer:       str     # outer / inner
    copper_oz:   float   # 铜厚 oz
    note:        str = ""

    def report(self) -> str:
        return (
            f"{'─'*46}\n"
            f"  Layer     : {self.layer.upper()}  "
            f"{'(⚠ 内层散热差, 线宽≈2×外层)' if self.layer=='inner' else '(外层,空气对流散热)'}\n"
            f"  Current   : {self.current} A\n"
            f"  ΔT(max)   : +{self.temp_rise} °C\n"
            f"  Copper    : {self.copper_oz} oz  "
            f"(≈ {self.thickness_um:.0f} μm / {self.thickness_mil:.2f} mil)\n"
            f"{'─'*46}\n"
            f"  ▶ Required cross-section : {self.area_mil2:.1f} mil²  "
            f"(≈ {self.area_mil2 * 0.000645:.2f} mm²)\n"
            f"  ▶ Min Trace Width         : {self.width_mil:.1f} mil  "
            f"= {self.width_mm:.2f} mm\n"
            f"{'  ⚠ ' + self.note if self.note else ''}"
            f"\n  📐 Recommended (rounded up to mfg-friendly): "
            f"{_nice_mil(self.width_mil)} mil = {_nice_mm(self.width_mm)} mm\n"
        )


def _nice_mil(v: float) -> str:
    """向上取整到工艺友好值"""
    for step in [6, 8, 10, 12, 15, 20, 25, 30, 40, 50, 60, 80, 100, 120, 150, 200]:
        if v <= step:
            return f"≥{step}"
    return f"≥{math.ceil(v/10)*10}"


def _nice_mm(v: float) -> str:
    return f"{_mil_to_mm(_mil_from_str(_nice_mil(v))):.2f}"


def _mil_to_mm(m: float) -> float:
    return m * MIL_TO_MM


def _mil_from_str(s: str) -> float:
    return float(s.replace("≥", ""))


# ── 核心：电流 → 线宽 ──────────────────────────────────
def current_to_width(
    current: float,
    temp_rise: float = 10.0,
    copper_oz: float = 1.0,
    layer: Literal["outer", "inner"] = "outer",
    *,
    safety_margin: float = 1.1,   # 工程安全系数(可选)
) -> TraceResult:
    """
    根据 IPC-2221 计算承载指定电流所需的最小走线宽度

    :param current:    目标电流 (A)
    :param temp_rise:  允许温升 °C（10=保守/高可靠, 20=通用, 30=空间受限）
    :param copper_oz:  铜厚 (oz)，常用 0.5 / 1.0 / 2.0
    :param layer:      'outer'(表层) 或 'inner'(内层)
    :param safety_margin: 乘在线宽上的安全余量（1.1 = +10%）
    :return: TraceResult
    """
    if current <= 0:
        raise ValueError("current must be > 0")
    if temp_rise <= 0:
        raise ValueError("temp_rise must be > 0")

    k = LAYER_K[layer]

    # Step 1: 所需截面积 A (mil²)
    A = (current / (k * (temp_rise ** B))) ** (1.0 / C)

    # Step 2: 铜厚 → mil
    thickness_mil = copper_oz * MIL_PER_OZ

    # Step 3: 线宽
    width_mil = A / thickness_mil
    width_mil *= safety_margin

    note = ""
    if width_mil < 6:
        note = "Calculated < 6mil; check your fab's min trace width!"
    if layer == "inner" and temp_rise <= 10 and width_mil > 200:
        note = "Inner layer needs very wide trace at low ΔT — consider moving to outer or use 2oz+"

    return TraceResult(
        area_mil2    = A,
        width_mil    = width_mil,
        width_mm     = width_mil * MIL_TO_MM,
        thickness_mil= thickness_mil,
        thickness_um = thickness_mil * 25.4,  # mil→μm: ×25.4
        current      = current,
        temp_rise    = temp_rise,
        layer        = layer,
        copper_oz     = copper_oz,
        note         = note,
    )


# ── 反向：线宽 + 铜厚 → 最大允许电流 ────────────────────
def width_to_current(
    width_mil: float,
    copper_oz: float = 1.0,
    temp_rise: float = 10.0,
    layer: Literal["outer", "inner"] = "outer",
) -> float:
    """已知线宽，算最多能走多少安培"""
    k = LAYER_K[layer]
    thickness_mil = copper_oz * MIL_PER_OZ
    A = width_mil * thickness_mil
    return k * (temp_rise ** B) * (A ** C)


# ── 批量对照表生成器 ────────────────────────────────────
# ── 电流容量检查 ─────────────────────────────────────────────
@dataclass
class CurrentCheckResult:
    """电流容量检查结果"""
    network_name: str           # 网络名称
    layer_name: str             # 层名
    calculated_current: float   # 用户配置的电流 (A)
    max_allowed_current: float  # 最大允许电流 (A)
    utilization: float          # 利用率 (0-1), >1 表示超限
    is_exceeded: bool           # 是否超过限制
    trace_width_mm: float       # 走线宽度
    copper_oz: float            # 铜厚 (oz)
    temp_rise: float            # 温升 (°C)
    message: str = ""           # 警告消息

    def to_dict(self) -> dict:
        return {
            "network_name": self.network_name,
            "layer_name": self.layer_name,
            "calculated_current": self.calculated_current,
            "max_allowed_current": self.max_allowed_current,
            "utilization": self.utilization,
            "is_exceeded": self.is_exceeded,
            "trace_width_mm": self.trace_width_mm,
            "copper_oz": self.copper_oz,
            "temp_rise": self.temp_rise,
            "message": self.message,
        }


def check_current_capacity(
    network_name: str,
    layer_name: str,
    calculated_current: float,
    trace_width_mm: float,
    copper_thickness_mm: float,
    temp_rise: float = 10.0,
    is_outer_layer: bool = True,
    safety_margin: float = 1.0,
) -> CurrentCheckResult:
    """
    检查走线电流容量是否满足要求

    :param network_name: 网络名称
    :param layer_name: 层名
    :param calculated_current: 用户配置的电流 (A)
    :param trace_width_mm: 走线宽度
    :param copper_thickness_mm: 铜厚度 (mm)
    :param temp_rise: 允许温升 (°C)
    :param is_outer_layer: 是否为外层
    :param safety_margin: 安全系数 (默认1.0，即100%利用率算超限)
    :return: CurrentCheckResult
    """
    # 铜厚转换为 oz
    copper_oz = copper_thickness_mm / OZ_TO_MM

    # 判断层级
    layer = "outer" if is_outer_layer else "inner"

    # 计算该走线能承载的最大电流
    max_current = width_to_current(
        width_mil=trace_width_mm / MIL_TO_MM,
        copper_oz=copper_oz,
        temp_rise=temp_rise,
        layer=layer,
    )

    # 计算利用率（考虑安全系数）
    utilization = (calculated_current * safety_margin) / max_current if max_current > 0 else float('inf')
    is_exceeded = utilization > 1.0

    # 生成警告消息
    message = ""
    if is_exceeded:
        message = (f"⚠️ [{network_name}@{layer_name}] 电流超限！ "
                   f"配置电流: {calculated_current:.2f}A, "
                   f"最大允许: {max_current:.2f}A "
                   f"(走线宽: {trace_width_mm:.2f}mm, "
                   f"铜厚: {copper_oz:.2f}oz, "
                   f"温升: {temp_rise}°C, "
                   f"层级: {layer})")
    elif utilization > 0.8:
        message = (f"⚠️ [{network_name}@{layer_name}] 电流接近上限: "
                   f"{utilization*100:.1f}% "
                   f"({calculated_current:.2f}A / {max_current:.2f}A)")

    return CurrentCheckResult(
        network_name=network_name,
        layer_name=layer_name,
        calculated_current=calculated_current,
        max_allowed_current=max_current,
        utilization=utilization,
        is_exceeded=is_exceeded,
        trace_width_mm=trace_width_mm,
        copper_oz=copper_oz,
        temp_rise=temp_rise,
        message=message,
    )


def print_table(
    currents=(0.5, 1, 1.5, 2, 3, 4, 5, 8, 10),
    oz_options=(0.5, 1.0, 2.0),
    delta_t=20,
    layer="outer",
):
    print(f"\n{'='*62}")
    print(f"  IPC-2221 对照表  |  ΔT={delta_t}°C  |  {layer.upper()} LAYER")
    print(f"{'='*62}")
    header = f"  {'I(A)':>5} | {'0.5oz→mil(mm)':>14} | {'1oz→mil(mm)':>14} | {'2oz→mil(mm)':>14}"
    print(header)
    print(f"  {'-'*58}")
    for I in currents:
        cols = []
        for oz in oz_options:
            r = current_to_width(I, temp_rise=delta_t, copper_oz=oz, layer=layer, safety_margin=1.0)
            cols.append(f"{r.width_mil:>5.1f} ({r.width_mm:>4.2f})")
        print(f"  {I:>5.1f} |  {cols[0]}  |  {cols[1]}  |  {cols[2]}")
    print()


# ── DEMO ───────────────────────────────────────────────
# if __name__ == "__main__":

#     # ══════════════════════════════════════════════════════
#     # 例1：最常见场景 — 1A / 2A / 3A @ 1oz 外层
#     # ══════════════════════════════════════════════════════
#     for I in [1.0, 2.0, 3.0]:
#         r = current_to_width(I, temp_rise=20, copper_oz=1.0, layer="outer")
#         print(r.report())

#     # ══════════════════════════════════════════════════════
#     # 例2：同样 3A — 内层 vs 外层对比
#     # ══════════════════════════════════════════════════════
#     print("\n╔═══════════════════════════════════════════════════╗")
#     print("║  3A / 1oz  ΔT=20°C — 外层 vs 内层 对比            ║")
#     print("╚═══════════════════════════════════════════════════╝")
#     for lay in ["outer", "inner"]:
#         r = current_to_width(3, temp_rise=20, copper_oz=1.0, layer=lay)
#         print(f"  {lay:5s} → {r.width_mil:.1f} mil = {r.width_mm:.2f} mm")

#     # ══════════════════════════════════════════════════════
#     # 例3：打印完整对照表（可直接贴进设计规范文档）
#     # ══════════════════════════════════════════════════════
#     print_table(currents=(0.5, 1, 1.5, 2, 3, 4, 5, 8, 10),
#                 oz_options=(0.5, 1.0, 2.0), delta_t=20, layer="outer")

#     print_table(currents=(0.5, 1, 1.5, 2, 3, 4, 5),
#                 oz_options=(1.0, 2.0), delta_t=20, layer="inner")