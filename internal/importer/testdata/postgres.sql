CREATE TABLE users (
 user_id SERIAL PRIMARY KEY, firstname TEXT NOT NULL, lastname TEXT NOT NULL,
 sex VARCHAR(1), birthdate DATE, email TEXT NOT NULL UNIQUE,
 password TEXT NOT NULL, username TEXT NOT NULL UNIQUE, allergens TEXT,
 diet TEXT, dislikes TEXT, lastmeal DATE, cooking_efforts TEXT,
 cooking_technique TEXT, haspref BOOLEAN
);
CREATE TABLE meals (
 meal_id BIGINT PRIMARY KEY, creator_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 creationdate DATE NOT NULL, recipetitle TEXT NOT NULL, yield TEXT,
 preptime TEXT, cooktime TEXT, totaltime TEXT, mychefnotes TEXT,
 equipment TEXT, instructions TEXT, allergens TEXT, ismeatorfish BOOLEAN,
 recipeimg TEXT, cloudinary_public_id TEXT, allergenssafetynote TEXT
);
CREATE TABLE ingredients (
 pk_ingredients SERIAL PRIMARY KEY, recipe_id BIGINT NOT NULL REFERENCES meals(meal_id) ON DELETE CASCADE,
 quantity NUMERIC, unit TEXT, ingredient_name TEXT NOT NULL, date DATE
);
