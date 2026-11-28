// Seeded generators for the mock libraries' files. Everything is derived from
// one PRNG so a restart shows the same library.

import type { FileStatus, HDRKind, JobState, StreamFacts } from "../../src/api/types.ts";

export interface MockFile {
  source_id: number;
  library: string;
  /** Relative to the library root. */
  path: string;
  size_bytes: number;
  container: string;
  video_codec: string;
  width: number;
  height: number;
  hdr: HDRKind;
  duration_seconds: number;
  audio: StreamFacts[];
  subtitles: StreamFacts[];
  status: FileStatus;
  job_state?: JobState;
  job_id?: number;
  attempt_count: number;
  output_size_bytes?: number;
  last_error?: string;
  created_at: number;
  updated_at: number;
  completed_at?: number;
  /** Set while status is "running": when the job and its current step began. */
  started_at?: number;
}

/** mulberry32: small, fast, and good enough for fixtures. */
export function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function weighted<T>(rand: () => number, table: readonly (readonly [T, number])[]): T {
  const total = table.reduce((sum, [, w]) => sum + w, 0);
  let roll = rand() * total;
  for (const [value, weight] of table) {
    roll -= weight;
    if (roll <= 0) return value;
  }
  return (table[table.length - 1] as readonly [T, number])[0];
}

const MOVIES = [
  "2001 A Space Odyssey (1968)", "Akira (1988)", "Alien (1979)", "Amelie (2001)", "Annihilation (2018)", "Arrival (2016)",
  "Barry Lyndon (1975)", "Before Sunrise (1995)", "Blade Runner (1982)", "Blade Runner 2049 (2017)", "Brazil (1985)", "Burning (2018)",
  "Casablanca (1942)", "Children of Men (2006)", "Chinatown (1974)", "City of God (2002)", "Collateral (2004)", "Contact (1997)",
  "Dark City (1998)", "Das Boot (1981)", "Drive (2011)", "Dune (2021)", "Dune Part Two (2024)", "Eternal Sunshine of the Spotless Mind (2004)",
  "Ex Machina (2014)", "Fargo (1996)", "Fight Club (1999)", "First Man (2018)", "Gattaca (1997)", "Ghost in the Shell (1995)",
  "Gravity (2013)", "Heat (1995)", "Her (2013)", "Hero (2002)", "High and Low (1963)", "In the Mood for Love (2000)",
  "Inception (2010)", "Interstellar (2014)", "Jaws (1975)", "Jurassic Park (1993)", "Lawrence of Arabia (1962)", "Leon (1994)",
  "Lost in Translation (2003)", "Mad Max Fury Road (2015)", "Memento (2000)", "Memories of Murder (2003)", "Metropolis (1927)", "Minority Report (2002)",
  "Moon (2009)", "Mulholland Drive (2001)", "No Country for Old Men (2007)", "Oldboy (2003)", "Oppenheimer (2023)", "Pan's Labyrinth (2006)",
  "Parasite (2019)", "Paris, Texas (1984)", "Paterson (2016)", "Perfect Blue (1997)", "Playtime (1967)", "Portrait of a Lady on Fire (2019)",
  "Princess Mononoke (1997)", "Prisoners (2013)", "Ran (1985)", "Rashomon (1950)", "Rear Window (1954)", "Roma (2018)",
  "Ronin (1998)", "Seven Samurai (1954)", "Sicario (2015)", "Solaris (1972)", "Spirited Away (2001)", "Stalker (1979)",
  "Sunshine (2007)", "Tenet (2020)", "The Apartment (1960)", "The Conversation (1974)", "The Fall (2006)", "The Grand Budapest Hotel (2014)",
  "The Handmaiden (2016)", "The Lighthouse (2019)", "The Lives of Others (2006)", "The Martian (2015)", "The Matrix (1999)", "The Northman (2022)",
  "The Prestige (2006)", "The Raid (2011)", "The Red Shoes (1948)", "The Revenant (2015)", "The Shining (1980)", "The Social Network (2010)",
  "The Thing (1982)", "The Third Man (1949)", "The Tree of Life (2011)", "There Will Be Blood (2007)", "Thief (1981)", "Tokyo Story (1953)",
  "Under the Skin (2013)", "Upstream Color (2013)", "Vertigo (1958)", "WALL-E (2008)", "Whiplash (2014)", "Yojimbo (1961)",
  "Your Name (2016)", "Zodiac (2007)", "Aftersun (2022)", "Decision to Leave (2022)", "Past Lives (2023)", "Perfect Days (2023)",
  "The Zone of Interest (2023)", "Anatomy of a Fall (2023)",
];

// The big library: enough episodes to test paging over thousands of rows.
const SERIES = [
  "Babylon 5", "Battlestar Galactica", "Better Call Saul", "Breaking Bad", "Chernobyl", "Columbo", "Community", "Dark", "Deadwood", "Deep Space Nine",
  "Fargo", "Firefly", "For All Mankind", "Fringe", "Halt and Catch Fire", "Hannibal", "Justified", "Mad Men", "Mr Robot", "Parks and Recreation",
  "Patriot", "Rome", "Scrubs", "Six Feet Under", "Station Eleven", "Succession", "The Americans", "The Expanse", "The Leftovers", "The Sopranos",
  "The Wire", "Twin Peaks", "Veep", "Watchmen", "Westworld",
];

const ANIME = ["Cowboy Bebop", "Frieren", "Fullmetal Alchemist Brotherhood", "Mushishi", "Planetes", "Samurai Champloo", "Steins Gate", "Vinland Saga"];

const SHOWS: readonly (readonly [string, number, number])[] = [
  ["Severance", 2, 9],
  ["Slow.Horses", 3, 6],
  ["Shogun", 1, 10],
  ["The.Bear", 2, 8],
  ["Andor", 1, 12],
  ["Blue.Eye.Samurai", 1, 8],
];

const RESOLUTIONS: readonly (readonly [readonly [number, number], number])[] = [
  [[3840, 2160], 20],
  [[1920, 1080], 60],
  [[1280, 720], 15],
  [[720, 480], 5],
];

const AUDIO: readonly (readonly [readonly [string, number], number])[] = [
  [["eac3", 6], 30],
  [["truehd", 8], 12],
  [["dts", 6], 18],
  [["ac3", 6], 14],
  [["aac", 2], 14],
  [["flac", 2], 6],
  [["opus", 6], 6],
];

/** What ffmpeg leaves on stderr when a source is damaged: long, multi-line, and worth reading in full. */
export const LONG_FFMPEG_ERROR = [
  "encode: ffmpeg exited with code 187: Error while decoding stream #0:0: Invalid data found when processing input",
  "[matroska,webm @ 0x55d0c8a2b440] Read error at pos. 7718342656 (0x1cc0c8000)",
  "[hevc @ 0x55d0c8a4e9c0] Could not find ref with POC 37",
  "[hevc @ 0x55d0c8a4e9c0] Error constructing the frame RPS.",
  "[hevc @ 0x55d0c8a4e9c0] Skipping invalid undecodable NALU: 1",
  "[vist#0:0/hevc @ 0x55d0c8a31d80] [dec:hevc @ 0x55d0c8a33300] Error submitting packet to decoder: Invalid data found when processing input",
  "[libsvtav1 @ 0x55d0c8a7f100] Error while filtering: Input/output error",
  "[vf#0:0 @ 0x55d0c8a35b00] Task finished with error code: -5 (Input/output error)",
  "[out#0/matroska @ 0x55d0c8a2e2c0] video:3982817KiB audio:402113KiB subtitle:71KiB other streams:0KiB global headers:0KiB muxing overhead: 0.071812%",
  "frame=128841 fps= 61 q=31.0 Lsize= 4388290KiB time=01:29:33.41 bitrate=6690.2kbits/s speed=2.54x",
  "Conversion failed!",
].join("\n");

const FAILURES = [
  LONG_FFMPEG_ERROR,
  "crf-search: no CRF between 18 and 45 reaches VMAF 95 below 80% of the source size",
  "probe: ffprobe exited with code 1: moov atom not found",
  "stage: destination filesystem has 3.1 GB free, the artifact needs at least 9.4 GB",
];

function stream(index: number, codec: string, language: string, channels?: number, extra: Partial<StreamFacts> = {}): StreamFacts {
  return { index, codec, language, title: "", ...(channels ? { channels } : {}), default: false, forced: false, ...extra };
}

function attributes(rand: () => number): Pick<MockFile, "container" | "video_codec" | "width" | "height" | "hdr" | "duration_seconds" | "audio" | "subtitles" | "size_bytes"> {
  const [width, height] = weighted(rand, RESOLUTIONS);
  const video_codec = weighted(rand, [["h264", 52], ["hevc", 30], ["av1", 9], ["mpeg4", 4], ["vc1", 3], ["mpeg2video", 2]] as const);
  const container = video_codec === "mpeg4" ? "avi" : weighted(rand, [["mkv", 82], ["mp4", 18]] as const);
  const hdr: HDRKind = height === 2160 && video_codec !== "h264" ? weighted(rand, [["hdr10", 50], ["dolby_vision", 25], ["", 20], ["hlg", 5]] as const) : "";
  const duration_seconds = Math.round(5000 + rand() * 4500);
  const [codec, channels] = weighted(rand, AUDIO);
  const audio = [stream(1, codec, "eng", channels, { default: true })];
  if (rand() < 0.35) audio.push(stream(2, "aac", weighted(rand, [["jpn", 3], ["deu", 2], ["fra", 2]] as const), 2));
  if (rand() < 0.15) audio.push(stream(audio.length + 1, "ac3", "eng", 2, { title: "Commentary" }));
  const subtitles: StreamFacts[] = [];
  if (rand() < 0.8) subtitles.push(stream(audio.length + 1, container === "mp4" ? "mov_text" : "subrip", "eng"));
  if (rand() < 0.3) subtitles.push(stream(audio.length + 2, "hdmv_pgs_subtitle", "eng", undefined, { forced: true }));
  // Megabits per second by height, shaved for the efficient codecs.
  const mbps = (height === 2160 ? 45 : height === 1080 ? 11 : height === 720 ? 5 : 2.2) * (0.6 + rand() * 0.8) * (video_codec === "av1" ? 0.4 : video_codec === "hevc" ? 0.65 : 1);
  return { container, video_codec, width, height, hdr, duration_seconds, audio, subtitles, size_bytes: Math.round((mbps * 1e6 * duration_seconds) / 8) };
}

function settle(file: MockFile, rand: () => number, now: number, weights: readonly (readonly [FileStatus, number])[]): void {
  const age = (maxDays: number) => now - Math.round(rand() * maxDays * 86_400_000);
  file.created_at = age(40);
  file.updated_at = file.created_at;
  // An AV1 source is what the default flow skips.
  file.status = file.video_codec === "av1" ? "skipped" : weighted(rand, weights);
  switch (file.status) {
    case "done":
      file.job_state = "complete";
      file.attempt_count = rand() < 0.1 ? 2 : 1;
      file.output_size_bytes = Math.round(file.size_bytes * (0.3 + rand() * 0.35));
      file.updated_at = file.completed_at = Math.max(file.created_at, age(20));
      break;
    case "queued":
      file.job_state = rand() < 0.06 ? "retrying" : "pending";
      file.attempt_count = file.job_state === "retrying" ? 1 : 0;
      break;
    case "failed":
      file.job_state = "failed";
      file.attempt_count = 3;
      file.last_error = FAILURES[Math.floor(rand() * FAILURES.length)];
      file.updated_at = file.completed_at = Math.max(file.created_at, age(6));
      break;
    case "skipped":
      file.job_state = rand() < 0.15 && file.video_codec !== "av1" ? "canceled" : "skipped";
      file.attempt_count = 1;
      file.last_error = file.job_state === "canceled" ? "canceled by operator" : file.video_codec === "av1" ? "skipped by flow: Already AV1" : "skipped: the encode was larger than the source";
      file.updated_at = file.completed_at = Math.max(file.created_at, age(25));
      break;
  }
}

/** What the first scan of a library added in the UI finds: a dozen files without jobs. */
export function discoverFiles(library: string, firstId: number, now: number): MockFile[] {
  const rand = prng(firstId);
  return Array.from({ length: 12 }, (_, i): MockFile => {
    const found = attributes(rand);
    const title = MOVIES[(firstId + i * 7) % MOVIES.length] as string;
    return { source_id: firstId + i, library, path: `${title}/${title}.${found.container}`, ...found, status: "none", attempt_count: 0, created_at: now, updated_at: now };
  });
}

export function generateFiles(now: number): MockFile[] {
  const rand = prng(0x0a17b11);
  const files: MockFile[] = [];
  const add = (library: string, path: string, weights: readonly (readonly [FileStatus, number])[]) => {
    const file: MockFile = { source_id: files.length + 1, library, path, ...attributes(rand), status: "none", attempt_count: 0, created_at: now, updated_at: now };
    settle(file, rand, now, weights);
    files.push(file);
    return file;
  };

  for (const title of MOVIES) {
    const file = add("movies", "", [["done", 46], ["queued", 30], ["skipped", 8], ["failed", 5], ["none", 11]]);
    file.path = `${title}/${title}.${file.container}`;
  }

  for (const [show, seasons, episodes] of SHOWS) {
    for (let s = 1; s <= seasons; s++) {
      for (let e = 1; e <= episodes; e++) {
        const tag = `S${String(s).padStart(2, "0")}E${String(e).padStart(2, "0")}`;
        const file = add("downloads", "", [["done", 55], ["queued", 30], ["failed", 3], ["none", 12]]);
        file.duration_seconds = Math.round(file.duration_seconds / 2.4);
        file.size_bytes = Math.round(file.size_bytes / 2.4);
        if (file.output_size_bytes) file.output_size_bytes = Math.round(file.output_size_bytes / 2.4);
        const release = `${show}.${tag}.${file.height}p.WEB-DL.${file.video_codec === "hevc" ? "x265" : file.video_codec === "av1" ? "AV1" : "x264"}`;
        file.path = `${release}/${release}.${file.container}`;
      }
    }
  }

  // tv: a large, mostly finished library. Its jobs are old, so the queue stays readable.
  for (const [index, show] of SERIES.entries()) {
    const seasons = 5 + (index % 4);
    for (let s = 1; s <= seasons; s++) {
      const episodes = s % 3 === 0 ? 22 : 26;
      for (let e = 1; e <= episodes; e++) {
        const file = add("tv", "", [["done", 86], ["queued", 3], ["skipped", 4], ["failed", 1], ["none", 6]]);
        file.duration_seconds = Math.round(file.duration_seconds / 2.4);
        file.size_bytes = Math.round(file.size_bytes / 2.4);
        if (file.output_size_bytes) file.output_size_bytes = Math.round(file.output_size_bytes / 2.4);
        const tag = `S${String(s).padStart(2, "0")}E${String(e).padStart(2, "0")}`;
        file.path = `${show}/Season ${String(s).padStart(2, "0")}/${show} - ${tag}.${file.container}`;
      }
    }
  }

  // anime: paused, so its queue only grows.
  for (const show of ANIME) {
    for (let e = 1; e <= 26; e++) {
      const file = add("anime", "", [["queued", 78], ["done", 12], ["none", 10]]);
      file.duration_seconds = 1440;
      file.size_bytes = Math.round(file.size_bytes / 4.5);
      if (file.output_size_bytes) file.output_size_bytes = Math.round(file.output_size_bytes / 4.5);
      if (file.audio[0]) file.audio[0].language = "jpn";
      file.path = `${show}/${show} - ${String(e).padStart(2, "0")}.${file.container}`;
    }
  }
  return files;
}
