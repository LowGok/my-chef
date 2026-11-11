// Fixture bytes stay outside modules with error boundaries: retained failure
// source frames must not reproduce a selected photo.
export const personalPhoto = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGMoWhAARAwQCgAnTgWJNEmEiAAAAABJRU5ErkJggg==",
  "base64",
);
