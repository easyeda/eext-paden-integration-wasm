[簡体中文](./README.md) | [English](./README.en.md) | [繁體中文](./README.zh-Hant.md) | [日本語](#) | [Русский](./README.ru.md)

# PADEN シミュレーション

JLCEDA & EasyEDA Pro 拡張機能 — PCB データを抽出し、PDN 電源配電ネットワーク FEM 分析を実行

> **バージョン**: 1.0.7 | **カテゴリー**: PCB | **キーワード**: PDN, Power Analysis, Simulation, 仿真, PI

## 機能

- EasyEDA から PCB トレース、ビア、パッド、銅箔 pour データを抽出
- ユーザー設定可能な電源レール（電圧源、電流負荷、層銅厚）
- 組み込み Go/WebAssembly 分析エンジン — 別途のバックエンドランタイムやサービスは不要
- Gerber 解析は tracespace、多角形のブール/オフセットは Clipper2-WASM、三角分割は earcut
- WebGL 電圧分布・電力密度ヒートマップ可視化
- 複数電源レール分析のサポート（1 回の結合求解 + N 回の個別求解）

## アーキテクチャ

```
EasyEDA PCB
    │
    ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────────────────┐
│  extract.ts │────▶│  convert.ts  │────▶│  ui/wasm-host.html      │
│  データ抽出   │     │  データ変換    │     │  Go/WASM ホスト IFrame  │
└─────────────┘     └──────────────┘     └─────────────────────────┘
                                                   │
                                                   ▼
                                         ┌─────────────────────────┐
                                         │  go-service/internal/   │
                                         │  pipeline               │
                                         │  ├ Gerber 解析          │
                                         │  ├ ジオメトリ処理        │
                                         │  │  (clipper2)          │
                                         │  ├ 三角分割             │
                                         │  └ FEM ソルバー         │
                                         └───────────┬─────────────┘
                                                     │
                                                     ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────────────────┐
│  display.ts │◀────│ wasmClient.ts│◀────│  JSON result            │
│  結果表示    │     │  WASM 通信    │     └─────────────────────────┘
└──────┬──────┘     └──────────────┘
       │
       ▼
┌─────────────┐
│ results.html│  WebGL 可視化
└─────────────┘
```

## 使用手順

### 1. JLCEDA Pro（3.2+）に本拡張機能をインストール
インストール後、設定を行います

![「設定」をクリック](./images/img-1.png)

![「外部との連携を許可」にチェック](./images/img-2.png)

### 2. エンジニアリングプロジェクトの PCB エディタで本拡張機能が使用可能

### 3. 上部メニューバーから 高度 → PADENシミュレーション → PDN分析を実行

### 4. 分析パラメータを選択し、「分析開始」をクリック

![PCB データ抽出](./images/img-3.png)

![PCB データ抽出](./images/img-4.png)

## クイックスタート

### 1. フロントエンド依存関係のインストール

```shell
npm install
```

### 2. 拡張機能のコンパイル

```shell
npm run compile
```

### 3. WASM 分析エンジンのビルド

```shell
npm run build:wasm-host-bridge
npm run build:wasm
```

完全なリリースビルド（TypeScript + WASM bridge + Go WASM + アセットコピー + `.eext` パッケージング）：

```shell
npm run build
```

開発用 WASM ビルド（シンボルを保持、ファイルサイズ大）：

```shell
npm run build:wasm:dev
```

### 4. EasyEDA へのインストール

1. JLCEDA Pro を開き、PCB エディタに入る
2. 拡張パッケージをインストール
3. メニューから **PDN 分析 → PDN 分析を実行...** を選択

> 外部バックエンドサービスは不要です。すべての分析は EasyEDA の WASM ランタイム内で実行されます。

## プロジェクト構成

```
├── src/                    # TypeScript フロントエンド
│   ├── index.ts            # メインエントリ、分析フロー統合
│   ├── extract.ts          # PCB データ抽出（トレース、ビア、パッド、銅箔）
│   ├── convert.ts          # データ変換と構成構築
│   ├── wasmClient.ts       # Go/WASM ホスト IFrame の読み込みと MessageBus 通信
│   ├── display.ts          # 結果表示（IFrame + Storage + MessageBus）
│   └── types.ts            # 型定義
├── ui/                     # ダイアログ HTML ファイル
│   ├── config.html         # 電源レール設定 UI
│   ├── results.html        # WebGL 可視化結果
│   ├── analyzing.html      # 分析進捗 UI
│   └── wasm-host.html      # 非表示の Go/WASM ホスト IFrame
├── go-service/             # Go/WebAssembly バックエンド
│   ├── main_wasm.go        # WASM エントリ、analyzeGerber JS API を公開
│   ├── internal/pipeline/  # 完全な分析パイプライン
│   ├── internal/problem/   # 問題定義（層、ネットワーク、ビア）
│   ├── internal/solver/    # FEM ソルバーと疎行列
│   ├── internal/mesh/      # メッシュと三角分割インターフェース
│   ├── internal/geometry/  # Gerber 解析、Clipper2、earcut ブリッジ
│   └── internal/wasmapi/   # 結果シリアライズ
├── config/                 # esbuild 設定
├── scripts/                # build:wasm / build:wasm-host-bridge / copy-wasm-assets
├── build/                  # `.eext` パッケージングスクリプト
├── dist/                   # ビルド出力（index.js、paden.wasm、wasm_exec.js など）
└── extension.json          # 拡張機能マニフェスト
```

## 使用フロー

1. **データ抽出** — 現在の PCB からトレース、ビア、パッド、銅箔エリアを抽出
2. **分析設定** — 電源ネットを選択し、電圧源と電流負荷を設定
3. **Gerber 解析** — Go/WASM エンジンが tracespace を介して Gerber ZIP から銅層ジオメトリを解析
4. **ジオメトリ処理** — Clipper2-WASM でブール演算とオフセット、earcut で三角分割
5. **FEM 解析** — ラプラシアン行列を構築して電圧分布を求解
6. **可視化** — WebGL 電圧ヒートマップ。レイヤー切替、メッシュエッジ表示、ビアマーカー対応

## 技術スタック

**フロントエンド**：TypeScript, esbuild, WebGL

**バックエンド**：Go 1.26+, WebAssembly, `syscall/js`

**ジオメトリ/メッシュ**：`@tracespace/parser`, `@tracespace/plotter`, `clipper2-wasm`, `earcut`

**依存関係**：`@jlceda/pro-api-types`, `@tracespace/parser`, `@tracespace/plotter`, `clipper2-wasm`, `earcut`

## ライセンス

本拡張機能は [Apache License 2.0](https://choosealicense.com/licenses/apache-2.0/) オープンソースライセンスを使用しています。

---

## リンク

- **ホームページ**: https://github.com/easyeda/eext-paden-integration
- **問題報告**: https://github.com/easyeda/eext-paden-integration/issues
