# セキュリティ・性能レビュー（2026-10-14）

コアAPI、ビットストリーム、各OSバックエンド、コンテナ、画像、録画・再生、HLS・WebRTC、別モジュールの依存構成を確認した。再現できる不具合を修正し、境界値と失敗時の回帰テストを追加した。

## 修正内容

| 領域 | 問題と修正 |
| --- | --- |
| 共通フレーム検証 | サイズ・strideの積が整数オーバーフローすると短いバッファを受理していた。`Frame.Validate`で除算を使って各planeを検証し、全エンコーダに適用。`RGBAImage`と画素変換にも適用した。 |
| 数値・時刻 | NaN・Infやネイティブ整数型へ収まらない設定値を拒否。時刻変換は128 bitの中間積で計算し、表現範囲外は飽和する。最近値丸めとゼロ方向の切り捨てを分け、既存の丸め規則を維持した。 |
| Annex-B・ビット読み取り | 大きなNALを読む際の全体再走査・固定量の再確保を解消。AUの無制限蓄積、`SkipBits`の加算オーバーフロー、parameter-set IDの範囲を検証した。`ReadBits`はバイト単位で処理する。 |
| AAC・ADTS | ADTS長の加算オーバーフロー、途中の音声形式変更、short writeの見逃し、ES descriptorの宣言長・終端・optional fieldの境界を修正した。 |
| 表示順 | Sendが`ErrAgain`やエラーになった際にもseq・POC・CRA状態が進んでいた。成功時だけ状態を確定し、通常フレームではparameter mapを複製しない。Flush失敗時も状態を維持する。 |
| VideoToolbox・WebCodecs | 出力待ちに加えて未完了の投入もキュー上限に算入。VideoToolboxは入力tokenで完了を追跡し、同期drop・非同期callback・失敗・再生成時に予約を解放する。 |
| MediaCodec | Flushまで全フレームのPTSを保持していた履歴を未出力分だけに縮小。負のbuffer offset、長さとlayoutのオーバーフロー、奇数幅NV12のstrideを修正した。 |
| AudioToolbox | キャンセルを確認し、変換失敗後の古いPCM・PTSを破棄する。設定途中で失敗したconverterの解放、native入出力サイズの検証、要求0の入力callbackを修正した。 |
| VA-API・Media Foundation | native planeのoffset・pitch・cropをコピー前に検証。VA-APIは行ごとの手動境界検査をplane単位の検証にまとめ、ハードウェア不要のテストを追加した。 |
| MP4・IVF・HEIF/AVIF | 欠けた必須box、壊れたsample/chunk table、実ファイルを超えるmdat/sample範囲、box・extent・OBUのサイズ計算を検証。HEIFのiref version 1のcount幅を修正し、grid余白分のcanvasと後続コピーを除去した。 |
| 録画・配信 | CaptureとCloseの競合によるclosed channelへの送信を防止。MP4の共有seek位置とWebRTCのpacket送信順を同期する。HLSは入力データを所有し、windowから外れたsegmentの参照を消す。 |
| 開発環境 | examplesの古い自己依存をv0.2.0へ揃え、欠けていたnet/webrtc依存を追加。`go mod tidy`でチェックサムを生成。crossbuildスクリプトがビルド失敗を見逃す問題を修正した。 |

## 性能測定

Apple M3で4 MiBのNALを読む同一ベンチマークを実行し、旧実装はGo overlayで比較した。

| | 修正前 | 修正後 |
| --- | ---: | ---: |
| 時間 | 103.3 ms/op | 0.714 ms/op |
| 割当量 | 149.0 MB/op | 20.9 MB/op |
| 割当回数 | 80 allocs/op | 11 allocs/op |

NAL読み込み部分で約145倍。コーデック全体の処理速度を示す測定ではない。

## 互換性上の変更

- Annex-B ReaderのAUサイズは既定64 MiB。大きなAUが必要な呼び出し元は、`Next`の前に`MaxAccessUnitSize`を設定する。超過時は`ErrAccessUnitTooLarge`になる。
- 不正な寸法・stride、非有限の設定値、native型へ収まらない値を早期に拒否する。`PixelFormat.FrameSize`は不正またはint範囲外のサイズで0を返す。
- MP4 trackのtime scaleは公開APIで扱える正のint32範囲に限定する。
- HEIFのextent合計が元データ全体より大きいitemは拒否する。同じ範囲の反復参照によるメモリ増幅を防ぐため、重複extentを大量に使うファイルも対象になる。

## 検証と残課題

| 検証 | 結果 |
| --- | --- |
| 4モジュールの通常テスト | `go test -p 1 ./... ./net/webrtc/... ./ebitenvideo/... ./examples/...` が通過 |
| 静的検査 | 同じ4モジュールの`go vet`と`git diff --check`が通過 |
| クロスビルド | macOS・Linux・Windows各amd64/arm64、js/wasmの7ターゲットが通過 |
| VA-API | `GOWORK=off sh scripts/vaapi_fake_driver_test.sh`の4設定すべて通過 |
| MediaCodec | `GOWORK=off go test -tags mediacodec_typecheck ./internal/mediacodec ./internal/mediacodec/sys`が通過 |
| WebCodecs | js/wasmをGoの`go_js_wasm_exec`でNode.js上実行し、担当パッケージのテストが通過 |
| AV1 fuzz | 10秒、1,267,157入力で異常なし |
| race検査 | `go test -race -p 1 ./... ./net/webrtc/... ./ebitenvideo/... ./examples/...`が全パッケージ通過 |

サンプルプレイヤーの固定300/600tick待機は、HEAD版でもrace付きのseek後に失敗した。deadline方式で測るとHEAD版で6.12秒かかって成功したため、条件と最終assertを保ち、最大10秒のdeadlineへ変更した。通常は条件成立時にすぐ終了する。

- 単一の巨大なPCM入力から大量のpacketが生成される場合、音声出力キューは上限を超え得る。任意長PCMの受け入れとFlush・Receiveの役割を含む設計が必要で、今回は変更していない。
- 画像の展開後バイト数には固定上限を設けていない。整数計算が収まる巨大gridは大きなcanvasを確保し得るため、未信頼画像の呼び出し元は入力長を制限し、`DecodeConfig`または`DecodeInfo`で画素数を確認する必要がある。ライブラリ側の設定可能な展開メモリ上限は残課題。
- HEIF/AVIFの`Decode(io.Reader)`と`DecodeConfig(io.Reader)`は全量読み込みする。入力長の制限は呼び出し元で行う。Playbackへ渡すReaderがReadで停止し続ける場合、Closeの終了時間も保証されない。
- Android実機、Windows実機、NVIDIA・Intel・AMDの実GPU、ブラウザ上での今回変更の実行確認は未実施。クロスビルド、型検査、Node.jsでのwasmテスト、偽VAドライバのテストとは区別する。
