

drop view if exists content_item_has_environment_hmd_lang_nsreporting;
create view content_item_has_environment_hmd_lang_nsreporting as
    select
        id,
        from_id,
        to_id,
        created_at, updated_at
    from relationship
    where is_deleted = false and name = 'hmd_lang_nsreporting.content_item_has_environment';