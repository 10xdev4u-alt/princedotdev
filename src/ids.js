import { customAlphabet } from "nanoid";

// 12 lowercase alphanumeric chars, URL-safe, in the same spirit as postplan's
// draft ids. Short enough to paste, long enough to be unguessable.
const draftId = customAlphabet("0123456789abcdefghijklmnopqrstuvwxyz", 12);
const internalId = customAlphabet("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", 20);

export function newDraftId() {
  return draftId();
}

export function newInternalId() {
  return internalId();
}
