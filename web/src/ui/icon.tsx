import { ICON_DATA } from "./icon-data";

export type IconName = keyof typeof ICON_DATA;

/** Every vendored icon id, for the gallery. */
export const ICONS = Object.keys(ICON_DATA) as IconName[];

/**
 * The kinds of flow node, as pkg/flow/catalog.go names them, plus "command"
 * from the design system. Kind is told by icon and title, never by colour.
 */
export const NODE_KINDS = {
  input: { icon: "file-input", label: "Input file" },
  condition: { icon: "split", label: "Condition" },
  video: { icon: "film", label: "Video" },
  audio: { icon: "audio-lines", label: "Audio" },
  subtitles: { icon: "captions", label: "Subtitles" },
  container: { icon: "package", label: "Container" },
  output: { icon: "file-output", label: "Output" },
  command: { icon: "square-terminal", label: "Command" },
} as const satisfies Record<string, { icon: IconName; label: string }>;

export type NodeKind = keyof typeof NODE_KINDS;

export interface IconProps {
  name: IconName;
  size?: number;
  /** Rotate continuously. Only for `loader-circle`; stops under prefers-reduced-motion. */
  spin?: boolean;
  /** Only when the icon stands alone and carries meaning. Otherwise it is hidden from assistive tech. */
  label?: string;
  strokeWidth?: number;
  class?: string;
}

/** A Lucide stroke icon in currentColor: 16px in controls, 14px in compact ones. */
export function Icon(props: IconProps) {
  return (
    <svg
      class={["av-icon", props.class, { "av-spin": props.spin }]}
      width={props.size ?? 16}
      height={props.size ?? 16}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width={props.strokeWidth ?? 2}
      stroke-linecap="round"
      stroke-linejoin="round"
      role={props.label ? "img" : undefined}
      aria-label={props.label}
      aria-hidden={props.label ? undefined : "true"}
      innerHTML={ICON_DATA[props.name]}
    />
  );
}

const MARK = "M2 14H61V25H52Q46 25 46 31V38L54 44V51H18V44L26 38V31Q26 26 21 25Z";
const WORDMARK =
  "M75.81 36.0H72.97Q71.81 36.0 70.91 35.46Q70.01 34.92 69.51 33.88Q69.01 32.84 69.01 31.44V31.0L70.29 32.4H68.85Q68.33 34.4 66.71 35.44Q65.09 36.48 62.77 36.48Q59.57 36.48 57.85 34.78Q56.13 33.08 56.13 30.32Q56.13 28.16 57.19 26.76Q58.25 25.36 60.25 24.64Q62.25 23.92 65.05 23.92H68.61V22.4Q68.61 20.68 67.69 19.7Q66.77 18.72 64.73 18.72Q62.93 18.72 61.83 19.5Q60.73 20.28 59.97 21.36L56.93 18.64Q58.09 16.84 60.01 15.74Q61.93 14.64 65.09 14.64Q69.29 14.64 71.51 16.58Q73.73 18.52 73.73 22.08V31.92H75.81ZM68.61 27.0H65.33Q63.33 27.0 62.33 27.66Q61.33 28.32 61.33 29.56V30.24Q61.33 31.48 62.17 32.12Q63.01 32.76 64.49 32.76Q65.65 32.76 66.57 32.42Q67.49 32.08 68.05 31.4Q68.61 30.72 68.61 29.76Z M84.69 36.0H79.57V15.12H84.69V18.6H84.89Q85.53 16.92 86.91 15.78Q88.29 14.64 90.73 14.64Q93.93 14.64 95.67 16.78Q97.41 18.92 97.41 22.8V36.0H92.29V23.32Q92.29 21.12 91.49 19.98Q90.69 18.84 88.85 18.84Q87.81 18.84 86.85 19.22Q85.89 19.6 85.29 20.34Q84.69 21.08 84.69 22.2Z M113.25 36.0H107.17L100.21 15.12H105.25L108.17 24.12L110.21 31.56H110.49L112.53 24.12L115.37 15.12H120.25Z M125.81 12.08Q124.25 12.08 123.53 11.34Q122.81 10.6 122.81 9.52V8.72Q122.81 7.6 123.53 6.88Q124.25 6.16 125.81 6.16Q127.37 6.16 128.09 6.88Q128.81 7.6 128.81 8.72V9.52Q128.81 10.6 128.09 11.34Q127.37 12.08 125.81 12.08ZM123.25 36.0V15.12H128.37V36.0Z M141.73 36.0H138.97Q136.37 36.0 135.11 34.68Q133.85 33.36 133.85 30.96V6.4H138.97V31.92H141.73Z";

export interface LogoProps {
  variant?: "mark" | "wordmark";
  /** Height in px. */
  size?: number;
  class?: string;
}

/** The anvil mark or the lowercase wordmark, in the surrounding CSS color. */
export function Logo(props: LogoProps) {
  return (
    <svg
      class={["av-logo", props.class]}
      width={props.variant === "wordmark" ? undefined : (props.size ?? 24)}
      height={props.size ?? 24}
      viewBox={props.variant === "wordmark" ? "0 0 144 48" : "0 0 64 64"}
      fill="currentColor"
      role="img"
      aria-label="anvil"
      innerHTML={props.variant === "wordmark" ? `<path transform="translate(-1.51 -2.59) scale(0.76)" d="${MARK}"/><path d="${WORDMARK}"/>` : `<path d="${MARK}"/>`}
    />
  );
}
