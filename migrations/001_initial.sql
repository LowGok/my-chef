CREATE TABLE users (
 user_id INTEGER PRIMARY KEY AUTOINCREMENT,
 firstname TEXT NOT NULL, lastname TEXT NOT NULL, sex TEXT, birthdate TEXT,
 email TEXT NOT NULL UNIQUE, password TEXT NOT NULL, username TEXT NOT NULL UNIQUE,
 allergens TEXT, diet TEXT, dislikes TEXT, lastmeal TEXT, cooking_efforts TEXT,
 cooking_technique TEXT, haspref BOOLEAN, preferences_json TEXT
);
CREATE TABLE menus (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 created_unix INTEGER NOT NULL
);
CREATE INDEX menus_owner ON menus(user_id,id);
CREATE TABLE generations (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 request_key TEXT NOT NULL, request_hash TEXT NOT NULL, request_json TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed')),
 stage TEXT NOT NULL, menu_id INTEGER REFERENCES menus(id),
 error_code TEXT, error_message TEXT, created_unix INTEGER NOT NULL DEFAULT 0,
 UNIQUE(user_id,request_key)
);
CREATE UNIQUE INDEX generations_one_active ON generations(user_id) WHERE status IN ('queued','running');
CREATE INDEX generations_owner ON generations(user_id,id);
CREATE TABLE meals (
 meal_id INTEGER PRIMARY KEY AUTOINCREMENT,
 creator_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 menu_id INTEGER REFERENCES menus(id) ON DELETE CASCADE,
 creationdate TEXT NOT NULL, recipetitle TEXT NOT NULL, yield TEXT,
 preptime TEXT, cooktime TEXT, mychefnotes TEXT, equipment TEXT, instructions TEXT,
 allergens TEXT, allergenssafetynote TEXT, ismeatorfish BOOLEAN, totaltime TEXT,
 recipeimg TEXT, cloudinary_public_id TEXT, tips_json TEXT, recipe_json TEXT,
 image_status TEXT NOT NULL DEFAULT 'unavailable' CHECK(image_status IN ('pending','ready','unavailable')),
 legacy_raw TEXT
);
CREATE INDEX meals_owner_date ON meals(creator_id,creationdate,meal_id);
CREATE INDEX meals_menu ON meals(menu_id,meal_id);
CREATE TABLE ingredients (
 pk_ingredients INTEGER PRIMARY KEY AUTOINCREMENT,
 recipe_id INTEGER NOT NULL REFERENCES meals(meal_id) ON DELETE CASCADE,
 quantity TEXT, unit TEXT, ingredient_name TEXT NOT NULL, date TEXT
);
CREATE INDEX ingredients_recipe ON ingredients(recipe_id,pk_ingredients);
CREATE TABLE sessions (
 hash TEXT PRIMARY KEY, user_id INTEGER REFERENCES users(user_id) ON DELETE CASCADE,
 expires_unix INTEGER NOT NULL
);
CREATE INDEX sessions_expiry ON sessions(expires_unix);
