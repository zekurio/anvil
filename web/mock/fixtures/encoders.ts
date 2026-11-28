// The host encoder report GET /encoders answers, from a machine with working
// VAAPI (HEVC and H.264) and no usable QSV or AMF. Reasons are the probe's
// own sentences, cut the way the captured output cuts them.

import type { EncoderProbeState, Encoders } from "../../src/api/types.ts";

const qsvReason = "Error creating a MFX session: -9. Device creation failed: -1313558101.";
const vaapiAV1Reason = "No usable encoding entrypoint found for profile VAProfileAV1Profile0 (32).";
const amfReason = "DLL libamfrt64.so.1 failed to open.";

export const ENCODER_PROBE: Encoders = {
  state: "ready",
  render_device: "/dev/dri/renderD128",
  encoders: [
    {
      name: "libsvtav1",
      codec: "av1",
      backend: "software",
      quality_label: "CRF",
      available: true,
      bit_depths: [
        { bit_depth: 8, available: true },
        { bit_depth: 10, available: true },
      ],
    },
    {
      name: "libx265",
      codec: "hevc",
      backend: "software",
      quality_label: "CRF",
      available: true,
      bit_depths: [
        { bit_depth: 8, available: true },
        { bit_depth: 10, available: true },
      ],
    },
    {
      name: "libx264",
      codec: "h264",
      backend: "software",
      quality_label: "CRF",
      available: true,
      bit_depths: [
        { bit_depth: 8, available: true },
        { bit_depth: 10, available: true },
      ],
    },
    {
      name: "av1_qsv",
      codec: "av1",
      backend: "qsv",
      quality_label: "ICQ",
      available: false,
      reason: `av1_qsv could not encode 8-bit av1: ${qsvReason}`,
      bit_depths: [
        { bit_depth: 8, available: false, reason: `av1_qsv could not encode 8-bit av1: ${qsvReason}` },
        { bit_depth: 10, available: false, reason: `av1_qsv could not encode 10-bit av1: ${qsvReason}` },
      ],
    },
    {
      name: "hevc_qsv",
      codec: "hevc",
      backend: "qsv",
      quality_label: "ICQ",
      available: false,
      reason: `hevc_qsv could not encode 8-bit hevc: ${qsvReason}`,
      bit_depths: [
        { bit_depth: 8, available: false, reason: `hevc_qsv could not encode 8-bit hevc: ${qsvReason}` },
        { bit_depth: 10, available: false, reason: `hevc_qsv could not encode 10-bit hevc: ${qsvReason}` },
      ],
    },
    {
      name: "h264_qsv",
      codec: "h264",
      backend: "qsv",
      quality_label: "ICQ",
      available: false,
      reason: `h264_qsv could not encode 8-bit h264: ${qsvReason}`,
      bit_depths: [{ bit_depth: 8, available: false, reason: `h264_qsv could not encode 8-bit h264: ${qsvReason}` }],
    },
    {
      name: "av1_vaapi",
      codec: "av1",
      backend: "vaapi",
      quality_label: "QP",
      available: false,
      reason: `av1_vaapi could not encode 8-bit av1: ${vaapiAV1Reason}`,
      bit_depths: [
        { bit_depth: 8, available: false, reason: `av1_vaapi could not encode 8-bit av1: ${vaapiAV1Reason}` },
        { bit_depth: 10, available: false, reason: `av1_vaapi could not encode 10-bit av1: ${vaapiAV1Reason}` },
      ],
    },
    {
      name: "hevc_vaapi",
      codec: "hevc",
      backend: "vaapi",
      quality_label: "QP",
      available: true,
      bit_depths: [
        { bit_depth: 8, available: true },
        // The one depth warning worth seeing: an encoder that works at 8 bits only.
        { bit_depth: 10, available: false, reason: "hevc_vaapi could not encode 10-bit hevc: the device has no P010 support." },
      ],
    },
    {
      name: "h264_vaapi",
      codec: "h264",
      backend: "vaapi",
      quality_label: "QP",
      available: true,
      bit_depths: [{ bit_depth: 8, available: true }],
    },
    {
      name: "av1_amf",
      codec: "av1",
      backend: "amf",
      quality_label: "QP",
      available: false,
      reason: `av1_amf could not encode 8-bit av1: ${amfReason}`,
      bit_depths: [
        { bit_depth: 8, available: false, reason: `av1_amf could not encode 8-bit av1: ${amfReason}` },
        { bit_depth: 10, available: false, reason: `av1_amf could not encode 10-bit av1: ${amfReason}` },
      ],
    },
    {
      name: "hevc_amf",
      codec: "hevc",
      backend: "amf",
      quality_label: "QP",
      available: false,
      reason: `hevc_amf could not encode 8-bit hevc: ${amfReason}`,
      bit_depths: [
        { bit_depth: 8, available: false, reason: `hevc_amf could not encode 8-bit hevc: ${amfReason}` },
        { bit_depth: 10, available: false, reason: `hevc_amf could not encode 10-bit hevc: ${amfReason}` },
      ],
    },
    {
      name: "h264_amf",
      codec: "h264",
      backend: "amf",
      quality_label: "QP",
      available: false,
      reason: `h264_amf could not encode 8-bit h264: ${amfReason}`,
      bit_depths: [{ bit_depth: 8, available: false, reason: `h264_amf could not encode 8-bit h264: ${amfReason}` }],
    },
  ],
};

/** The report in one probe state. Only a ready report lists encoders. */
export function encodersView(state: EncoderProbeState): Encoders {
  if (state === "ready") return ENCODER_PROBE;
  return {
    state,
    render_device: ENCODER_PROBE.render_device,
    encoders: [],
    ...(state === "failed" ? { reason: "ffmpeg could not list its encoders." } : {}),
  };
}
