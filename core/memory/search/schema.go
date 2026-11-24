package search

// migrations are the search tables, applied by db.Migrate under the name "search". A step, once released, is never
// edited: a change is a new step at the end. The tables carry a search_ prefix because the page store shares the
// database.
var migrations = []string{
	// vec and q8 are null until the model has seen the text; model says which one did. context is the breadcrumb
	// (title › heading › subheading), indexed with the chunk and embedded with it.
	`create table search_chunks (
  id integer primary key, path text not null, ord integer not null, title text not null, context text not null default '',
  text text not null, heading text not null default '', model text, vec blob, q8 blob, unique (path, ord));
-- what the embedder still has to do: the chunks of no model, or of another one
create index search_chunks_model on search_chunks(model);
create virtual table search_chunks_fts using fts5(
  title, text, context, content = 'search_chunks', content_rowid = 'id', tokenize = 'unicode61 remove_diacritics 2');
create trigger search_chunks_ai after insert on search_chunks begin
  insert into search_chunks_fts (rowid, title, text, context) values (new.id, new.title, new.text, new.context);
end;
create trigger search_chunks_ad after delete on search_chunks begin
  insert into search_chunks_fts (search_chunks_fts, rowid, title, text, context)
    values ('delete', old.id, old.title, old.text, old.context);
end;
create trigger search_chunks_au after update of title, text, context on search_chunks begin
  insert into search_chunks_fts (search_chunks_fts, rowid, title, text, context)
    values ('delete', old.id, old.title, old.text, old.context);
  insert into search_chunks_fts (rowid, title, text, context) values (new.id, new.title, new.text, new.context);
end;`,
	// collections of documents. A document's chunks sit in search_chunks under the path
	// «doc:<id>», with where they sit in the source (locator: PDF page, heading, URL). A collection has a version and a
	// hub page (kind source) that stands for it in the graph; a document belongs to the version it was written under,
	// and one replaced is `superseded` (kept, searched by version). origin and followed are the collection's
	// follow state, carried from today's server; the core does not follow origins itself.
	`alter table search_chunks add column locator text not null default '{}';
update search_chunks set locator = json_object('heading', heading) where heading <> '';
create table search_collections (
  id text primary key, name text not null unique, version text not null default '', hub text, origin text,
  important integer not null default 0, followed text, created text not null, updated text not null);
create table search_documents (
  id text primary key, collection text not null references search_collections(id) on delete cascade,
  kind text not null, title text not null, origin text not null, hash text not null,
  added text not null, updated text not null, version text not null default '', superseded text,
  seen_version text not null default '');
create unique index search_documents_live on search_documents(collection, origin) where superseded is null;
create index search_documents_collection on search_documents(collection);
-- the original file of a document, kept beside its text; one row per document id, so a superseded one keeps its own
create table search_document_originals (
  document text primary key references search_documents(id) on delete cascade,
  name text not null, type text not null, size integer not null, sha256 text not null, bytes blob not null);`,
}
