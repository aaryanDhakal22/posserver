import { toast } from '$lib/toast.svelte';

/** Copy text and say so (or say why not). */
export async function copyText(text: string, what = 'Copied'): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(what);
  } catch {
    toast.error("Couldn't copy — select the text and copy it by hand.");
  }
}

const WORDS = ['plum', 'river', 'tide', 'maple', 'cedar', 'harbor', 'lumen', 'quartz', 'ember', 'basil', 'falcon', 'meadow', 'copper', 'willow', 'saffron', 'pepper'];

/** A readable generated password, e.g. "plum-river-42-tide". */
export function generatePassword(): string {
  const r = new Uint32Array(4);
  crypto.getRandomValues(r);
  const w = (i: number) => WORDS[r[i] % WORDS.length];
  return `${w(0)}-${w(1)}-${10 + (r[2] % 90)}-${w(3)}`;
}
