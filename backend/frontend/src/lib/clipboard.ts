import { toast } from '$lib/toast.svelte';

/** Copy text and say so (or say why not). */
export async function copyText(text: string, what = 'Copied'): Promise<void> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
    } else if (!legacyCopy(text)) {
      throw new Error('copy refused');
    }
    toast.success(what);
  } catch {
    toast.error("Couldn't copy — select the text and copy it by hand.");
  }
}

// navigator.clipboard only exists on HTTPS (or localhost); staging is plain
// HTTP on the LAN, so fall back to a hidden textarea + execCommand('copy').
// The textarea goes inside an open modal <dialog> when there is one — focus
// can't leave a modal, so a textarea outside it couldn't be selected.
function legacyCopy(text: string): boolean {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;';
  const host = document.querySelector('dialog[open]') ?? document.body;
  host.appendChild(ta);
  const prev = document.activeElement as HTMLElement | null;
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  prev?.focus?.();
  return ok;
}

const WORDS = ['plum', 'river', 'tide', 'maple', 'cedar', 'harbor', 'lumen', 'quartz', 'ember', 'basil', 'falcon', 'meadow', 'copper', 'willow', 'saffron', 'pepper'];

/** A readable generated password, e.g. "plum-river-42-tide". */
export function generatePassword(): string {
  const r = new Uint32Array(4);
  crypto.getRandomValues(r);
  const w = (i: number) => WORDS[r[i] % WORDS.length];
  return `${w(0)}-${w(1)}-${10 + (r[2] % 90)}-${w(3)}`;
}
