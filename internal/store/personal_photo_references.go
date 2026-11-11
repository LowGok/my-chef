package store

// Every retained application snapshot participates, including interrupted and
// completed operations. References never depend on a still-existing library.
const personalPhotoReferences = `
EXISTS(SELECT 1 FROM personal_recipe_versions v JOIN personal_recipes r ON r.id=v.recipe_id WHERE v.photo_id=personal_photo_assets.id AND r.user_id=personal_photo_assets.user_id)
OR EXISTS(SELECT 1 FROM meals m WHERE m.creator_id=personal_photo_assets.user_id AND json_extract(m.personal_source,'$.photoId')=personal_photo_assets.id)
OR EXISTS(SELECT 1 FROM favorites f WHERE f.user_id=personal_photo_assets.user_id AND CASE WHEN json_valid(f.body) THEN json_extract(f.body,'$.source.photoId') END=personal_photo_assets.id)
OR EXISTS(SELECT 1 FROM favorite_queue q WHERE q.user_id=personal_photo_assets.user_id AND CASE WHEN json_valid(q.body) THEN json_extract(q.body,'$.source.photoId') END=personal_photo_assets.id)
OR EXISTS(SELECT 1 FROM personal_recipe_queue q WHERE q.user_id=personal_photo_assets.user_id AND json_extract(q.body,'$.source.photoId')=personal_photo_assets.id)
OR EXISTS(SELECT 1 FROM favorite_swaps f WHERE f.user_id=personal_photo_assets.user_id AND CASE WHEN json_valid(f.body) THEN json_extract(f.body,'$.source.photoId') END=personal_photo_assets.id)
OR EXISTS(SELECT 1 FROM generations g,json_each(CASE WHEN json_valid(g.request_json) THEN g.request_json ELSE '{}' END,'$.favorites') f WHERE g.user_id=personal_photo_assets.user_id AND json_extract(f.value,'$.recipe.source.photoId')=personal_photo_assets.id)
`
